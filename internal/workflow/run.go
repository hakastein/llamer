// Package workflow — прогон workflow: этапы по порядку конфига, ответ этапа
// доступен шаблонам следующих, события и результат — строками JSON в вывод.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/cost"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/redact"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Run — прогон workflow.
type Run struct {
	Context   context.Context
	Workflow  *config.Workflow
	Drivers   map[string]drivers.Driver
	Artifacts *artifacts.Run
	Cwd       string    // каталог контекста: рабочий каталог агента
	Out       io.Writer // события и результат строками JSON
	// Bin — каталог команд этапа: обёртки клиента команд и сокеты этапов; ClientDir — каталог бинаря llamer.
	Bin, ClientDir string
	commands       *commands
}

// Failure — этап, сорвавший прогон.
type Failure struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Stage — итог этапа: ответ и расход.
type Stage struct {
	Name          string          `json:"name"`
	Driver        string          `json:"driver,omitempty"` // у этапа-команды драйвера, модели и усилия нет
	Model         string          `json:"model,omitempty"`
	Effort        string          `json:"effort,omitempty"`
	OK            bool            `json:"ok"`
	Answer        json.RawMessage `json:"answer"` // JSON по схеме этапа, строка без схемы, null у сорванного
	Cost          *float64        `json:"cost"`   // null — драйвер цены не назвал, у этапа-команды — 0
	Tokens        int64           `json:"tokens"`
	ContextTokens int64           `json:"contextTokens"`
	Turns         int             `json:"turns"`
	DurationMs    int64           `json:"durationMs"`
}

// Result — последняя строка вывода и result.json прогона.
type Result struct {
	Type    string       `json:"type"`
	OK      bool         `json:"ok"`
	Run     string       `json:"run"`
	Failure *Failure     `json:"failure"`
	Stages  []Stage      `json:"stages"`
	Cost    cost.Summary `json:"cost"`
}

// out — вывод прогона. Вымарывание здесь, в одной точке: в строку попадают
// поток агента и его ответы.
type out struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *out) emit(v any) { o.write(line(v)) }

func (o *out) write(line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Читатель вывода — сервис; закрытый им вывод прогон не останавливает: итог останется в result.json.
	io.WriteString(o.w, line)
}

// line — строка вывода: JSON без секретов.
func line(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return redact.Secrets(string(data)) + "\n"
}

// stream — поток этапа: в артефакты и в вывод событием с именем этапа.
type stream struct {
	agent.Stream
	out   *out
	stage string
}

func (s *stream) Write(e *agent.Event) {
	s.Stream.Write(e)
	s.out.emit(map[string]any{"type": "event", "stage": s.stage, "event": e})
}

// Execute — волны по порядку до первой с сорванным этапом. Прерванный прогон
// результата не выдаёт: возвращается ошибка контекста.
func (r *Run) Execute() (*Result, error) {
	o := &out{w: r.Out}
	r.commands = &commands{workflow: r.Workflow, journal: newJournal(r.Artifacts.Path)}
	result := &Result{Type: "result", OK: true, Run: r.Artifacts.Path, Stages: []Stage{}}
	answers := map[string]string{}
	var records []*cost.Stage
	for _, wave := range r.Workflow.Waves {
		done, err := r.wave(wave, answers, o)
		if err != nil {
			return nil, err
		}
		if err := r.Context.Err(); err != nil {
			return nil, err
		}
		for _, f := range done {
			if f.record != nil {
				records = append(records, f.record)
			}
			result.Stages = append(result.Stages, f.stage)
			if f.failure != nil && result.Failure == nil {
				result.OK, result.Failure = false, f.failure
			}
		}
		if !result.OK {
			break
		}
		for i, s := range wave {
			answers[s.Name] = done[i].answer
		}
	}
	result.Cost = cost.Summarize(records)
	cost.Report(r.Artifacts.Path, records, logx.SinceStart())
	// Результат в выводе — только записанный: незаписанный срывает прогон.
	text := line(result)
	if _, err := r.Artifacts.SaveText("result.json", text); err != nil {
		return nil, err
	}
	o.write(text)
	return result, nil
}

// finished — итог этапа: строка результата, ответ для заданий следующих волн, запись расхода агента
// (nil у этапа-команды) и срыв (nil у прошедшего).
type finished struct {
	stage   Stage
	answer  string
	record  *cost.Stage
	failure *Failure
}

// wave — этапы волны одновременно. Первый ведёт, остальные стартуют с началом его ответа: общую часть
// заданий он к этому времени записал в кэш промпта, и они читают её оттуда, а не пишут каждый сам.
// Ждать приходится секунды.
func (r *Run) wave(stages []*config.WorkflowStage, answers map[string]string, o *out) ([]finished, error) {
	done := make([]finished, len(stages))
	errs := make([]error, len(stages))
	responding := make(chan struct{})
	lead := sync.OnceFunc(func() { close(responding) })
	var wg sync.WaitGroup
	for i, s := range stages {
		onResponse := func() {}
		if i == 0 {
			onResponse = lead
		} else if i == 1 {
			<-responding
		}
		wg.Go(func() {
			// Ведущий, так и не услышавший модель (сорвался, этап-команда), волну не держит.
			defer onResponse()
			done[i], errs[i] = r.run(s, answers, o, onResponse)
		})
	}
	wg.Wait()
	return done, errors.Join(errs...)
}

// run — этап агента или этап-команда.
func (r *Run) run(s *config.WorkflowStage, answers map[string]string, o *out, onResponse func()) (finished, error) {
	if s.Command != nil {
		return r.command(s), nil
	}
	record, detail, err := r.stage(s, answers, o, onResponse)
	if err != nil {
		return finished{}, err
	}
	out := record.Outcome
	f := finished{record: record, stage: Stage{Name: s.Name, Driver: s.Driver, Model: out.Model, Effort: out.Effort, OK: record.OK,
		Answer: json.RawMessage("null"), Cost: out.Cost, Tokens: out.Tokens(), ContextTokens: out.ContextTokens, Turns: out.Turns,
		DurationMs: out.DurationMs}}
	if !record.OK {
		f.failure = &Failure{Stage: s.Name, Reason: record.Reason, Detail: detail}
		return f, nil
	}
	f.answer = out.Result.Text
	f.stage.Answer = json.RawMessage(out.Result.Text)
	if s.Schema == nil {
		f.stage.Answer, _ = json.Marshal(out.Result.Text) // строка кодируется всегда
	}
	return f, nil
}

// command — этап-команда: команда workflow на стороне llamer, без агента, с записью в журнал прогона.
// Ответ — её stdout; отказ в разрешении, незаписанный результат или ненулевой код срывают этап.
func (r *Run) command(s *config.WorkflowStage) finished {
	logx.Logf("этап «%s» пошёл: команда %s", s.Name, s.Command[0])
	started := time.Now()
	reply, err := r.commands.call(r.Context, s, broker.Call{Argv: s.Command, Cwd: r.Cwd})
	f := finished{stage: Stage{Name: s.Name, OK: true, Answer: json.RawMessage("null"), Cost: new(float64),
		DurationMs: time.Since(started).Milliseconds()}}
	switch {
	case err != nil:
		f.failure = &Failure{Stage: s.Name, Reason: "command", Detail: err.Error()}
	case reply.Exit != 0:
		f.failure = &Failure{Stage: s.Name, Reason: "command", Detail: strings.TrimSpace(reply.Stderr)}
	}
	if f.failure != nil {
		logx.Logf("этап «%s» сорван: %s", s.Name, f.failure.Detail)
		f.stage.OK = false
		return f
	}
	f.answer = reply.Stdout
	f.stage.Answer, _ = json.Marshal(reply.Stdout) // строка кодируется всегда
	return f
}

// stage — этап агента на своём драйвере; detail — подробность срыва; onResponse — начало ответа модели.
func (r *Run) stage(s *config.WorkflowStage, answers map[string]string, o *out, onResponse func()) (*cost.Stage, string, error) {
	var prompt strings.Builder
	if err := s.Template.Execute(&prompt, config.PromptData{Answers: answers}); err != nil {
		return nil, "", err
	}
	promptPath := r.Artifacts.SavePrompt(s.Name, prompt.String())
	d := r.Drivers[s.Driver]
	sandbox := &drivers.Sandbox{Readable: r.Workflow.Readable}
	var env []string
	// shortcut: сокет в каталоге команд этапа — путь до него не длиннее 107 байт только при коротком TMPDIR;
	// переносить, если упрётся.
	socket := filepath.Join(r.Bin, s.Name+".sock")
	if r.Workflow.Commands != nil {
		// Клиенту в песочнице нужны его обёртка с сокетом и сам бинарь llamer.
		sandbox.Readable = slices.Concat(r.Workflow.Readable, []string{r.Bin, r.ClientDir})
		env = []string{broker.SocketEnv + "=" + socket}
	}
	// Первый отказ команды; этап срывается им после агента.
	failed := make(chan error, 1)
	stop := func() {}
	if r.Workflow.Commands != nil {
		var err error
		if stop, err = broker.Listen(socket, r.commands.handler(s, failed)); err != nil {
			return nil, "", err
		}
	}
	logx.Logf("этап «%s» пошёл: %s (%s/%s) → %s", s.Name, s.Driver, s.Model, s.Effort, promptPath)
	telemetry := agent.NewTelemetry(s.Name, nil, s.Skills, r.Cwd, &stream{r.Artifacts.OpenStream(s.Name), o, s.Name}, onResponse)
	outcome, err := d.Run(r.Context, drivers.Args{Name: s.Name, Model: s.Model, Effort: s.Effort, Prompt: prompt.String(), Skills: s.Skills,
		Allow: s.Allow, Cwd: r.Cwd, Schema: s.SchemaPath, Env: env, Sandbox: sandbox, Timeout: s.Timeout}, telemetry)
	stop()
	if err != nil {
		telemetry.Close()
		return nil, "", err
	}
	telemetry.Finish(outcome)
	record := &cost.Stage{Name: s.Name, Driver: s.Driver, OK: outcome.OK, Reason: outcome.Reason, Outcome: outcome}
	if !record.OK {
		detail := ""
		if outcome.Result != nil {
			detail = outcome.Result.Text
		}
		return record, detail, nil
	}
	select {
	case err := <-failed:
		logx.Logf("этап «%s» сорван: %v", s.Name, err)
		record.OK, record.Reason = false, "command"
		return record, err.Error(), nil
	default:
	}
	if s.Schema == nil {
		return record, "", nil
	}
	if detail := check(s.Schema, outcome.Result.Text); detail != "" {
		logx.Logf("этап «%s» сорван: ответ не по схеме %s", s.Name, s.SchemaPath)
		record.OK, record.Reason = false, "schema"
		return record, detail, nil
	}
	return record, "", nil
}

// check — ответ по схеме этапа; пустая строка — ответ годен, иначе — что не так.
func check(schema *jsonschema.Schema, answer string) string {
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(answer))
	if err != nil {
		return "ответ не JSON: " + err.Error()
	}
	if err := schema.Validate(value); err != nil {
		return err.Error()
	}
	return ""
}
