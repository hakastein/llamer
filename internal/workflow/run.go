// Package workflow — прогон workflow: этапы по порядку конфига, ответ этапа
// доступен шаблонам следующих, события и результат — строками JSON в вывод.
package workflow

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"

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
	Drivers   map[string]drivers.Runner
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
	Driver        string          `json:"driver"`
	Model         string          `json:"model"`
	Effort        string          `json:"effort"`
	OK            bool            `json:"ok"`
	Answer        json.RawMessage `json:"answer"` // JSON по схеме этапа, строка без схемы, null у сорванного
	Cost          *float64        `json:"cost"`   // null — драйвер цены не назвал
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

// Execute — этапы по порядку до первого сорванного. Прерванный прогон
// результата не выдаёт: возвращается ошибка контекста.
func (r *Run) Execute() (*Result, error) {
	o := &out{w: r.Out}
	r.commands = &commands{workflow: r.Workflow, journal: newJournal(r.Artifacts.Path)}
	result := &Result{Type: "result", OK: true, Run: r.Artifacts.Path, Stages: []Stage{}}
	answers := map[string]string{}
	var records []*cost.Stage
	for _, s := range r.Workflow.Stages {
		record, detail, err := r.stage(s, answers, o)
		if err != nil {
			return nil, err
		}
		if err := r.Context.Err(); err != nil {
			return nil, err
		}
		records = append(records, record)
		out := record.Outcome
		stage := Stage{Name: s.Name, Driver: s.Driver, Model: out.Model, Effort: out.Effort, OK: record.OK, Answer: json.RawMessage("null"),
			Cost: out.Cost, Tokens: out.Tokens(), ContextTokens: out.ContextTokens, Turns: out.Turns, DurationMs: out.DurationMs}
		if !record.OK {
			result.Stages = append(result.Stages, stage)
			result.OK, result.Failure = false, &Failure{Stage: s.Name, Reason: record.Reason, Detail: detail}
			break
		}
		answers[s.Name] = out.Result.Text
		stage.Answer = json.RawMessage(out.Result.Text)
		if s.Schema == nil {
			stage.Answer, _ = json.Marshal(out.Result.Text) // строка кодируется всегда
		}
		result.Stages = append(result.Stages, stage)
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

// stage — этап на своём драйвере; detail — подробность срыва.
func (r *Run) stage(s *config.WorkflowStage, answers map[string]string, o *out) (*cost.Stage, string, error) {
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
		if stop, err = broker.Listen(socket, r.commands.handler(s.Name, failed)); err != nil {
			return nil, "", err
		}
	}
	logx.Logf("этап «%s» пошёл: %s (%s/%s) → %s", s.Name, s.Driver, s.Level.Model, s.Level.Effort, promptPath)
	telemetry := agent.NewTelemetry(s.Name, nil, s.Skills, r.Cwd, &stream{r.Artifacts.OpenStream(s.Name), o, s.Name}, func() {})
	outcome, err := d.Run(r.Context, drivers.LaunchArgs{Stage: s.Name, Level: s.Level, Prompt: prompt.String(), Skills: s.Skills,
		Cwd: r.Cwd, Schema: s.SchemaPath, Env: env, Sandbox: sandbox, Timeout: s.Timeout}, telemetry)
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
