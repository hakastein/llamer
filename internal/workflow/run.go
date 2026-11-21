// Package workflow — прогон workflow: cliche-скрипт из `workflows/` каталога llamer зовёт этапы агента через модуль
// `llamer` и получает их ответы значениями; события и результат — строками JSON в вывод.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/cost"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/redact"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Run — прогон workflow.
type Run struct {
	Context context.Context
	Project *Project
	// Driver — драйвер по имени; готовит его к первому этапу на нём.
	Driver    func(name string) (drivers.Driver, error)
	Artifacts *artifacts.Run
	Cwd       string    // каталог контекста: рабочий каталог агента и программ workflow
	Out       io.Writer // события и результат строками JSON
	// Bin — каталог команд этапа: обёртки клиента команд и сокеты этапов; ClientDir — каталог бинаря llamer.
	Bin, ClientDir string
	// Listen обслуживает вызовы команд этапа на сокете, пока не вызван stop.
	Listen func(socket string, handle func(context.Context, broker.Call) broker.Reply) (stop func(), err error)

	out      *out
	commands *commands
	mu       sync.Mutex
	names    map[string]bool // имена этапов, уже вызванных скриптом
	stages   sync.WaitGroup  // запущенные этапы
	result   *Result
	records  []*cost.Stage
	fatal    error // ошибка, после которой прогон не выдаёт результата
}

// Failure — то, что сорвало прогон: этап или скрипт.
type Failure struct {
	Stage  string `json:"stage"` // пусто — ошибка скрипта вне этапа
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
	// Value — что вернул скрипт; null — у сорванного прогона.
	Value json.RawMessage `json:"value"`
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

// Execute — скрипт workflow с разобранным вызовом in. Прерванный прогон результата не выдаёт: возвращается
// ошибка контекста.
func (r *Run) Execute(workflow *cliche.Command, in cliche.Input) (*Result, error) {
	r.out = &out{w: r.Out}
	r.commands = &commands{catalog: r.Project.Commands, journal: newJournal(r.Artifacts.Path)}
	r.names = map[string]bool{}
	r.result = &Result{Type: "result", OK: true, Run: r.Artifacts.Path, Stages: []Stage{}, Value: json.RawMessage("null")}
	value, err := cliche.Run(r.Context, workflow, in, cliche.Env{Modules: map[string]any{"llamer": r.module()}})
	// Этапы, которых скрипт не дождался, прерывает конец cliche.Run; их итог — тоже итог прогона.
	r.stages.Wait()
	if r.fatal != nil {
		return nil, r.fatal
	}
	if err := r.Context.Err(); err != nil {
		return nil, err
	}
	if err == nil && r.result.OK {
		r.result.Value, err = valueJSON(value)
	}
	if err != nil {
		logx.Logf("скрипт workflow сорван: %v", err)
		r.fail(&Failure{Reason: "script", Detail: err.Error()})
	}
	r.result.Cost = cost.Summarize(r.records)
	cost.Report(r.Artifacts.Path, r.records, logx.SinceStart())
	// Результат в выводе — только записанный: незаписанный срывает прогон.
	text := line(r.result)
	if _, err := r.Artifacts.SaveText("result.json", text); err != nil {
		return nil, err
	}
	r.out.write(text)
	return r.result, nil
}

// valueJSON — значение, которое вернул скрипт, в JSON.
func valueJSON(value any) (json.RawMessage, error) {
	plain, err := plainOf(value)
	if err != nil {
		return nil, fmt.Errorf("скрипт вернул значение, которое не записать в JSON: %w", err)
	}
	return json.Marshal(plain)
}

// fail — срыв прогона; запоминается первый.
func (r *Run) fail(f *Failure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.OK {
		r.result.OK, r.result.Failure = false, f
	}
}

// failure — срыв прогона; nil — прогон идёт.
func (r *Run) failure() *Failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result.Failure
}

// launch — этап s на его драйвере: ответ — значение JSON при схеме, иначе строка; responding — начало ответа модели.
// Ошибка — этап сорван, и прогон неуспешен.
func (r *Run) launch(ctx context.Context, s *stage, responding func()) (any, error) {
	record, detail, err := r.stage(ctx, s, responding)
	if err != nil {
		r.mu.Lock()
		r.fatal = err
		r.mu.Unlock()
		return nil, err
	}
	o := record.Outcome
	summary := Stage{Name: s.Name, Driver: s.Driver, Model: o.Model, Effort: o.Effort, OK: record.OK,
		Answer: json.RawMessage("null"), Cost: o.Cost, Tokens: o.Tokens(), ContextTokens: o.ContextTokens, Turns: o.Turns,
		DurationMs: o.DurationMs}
	var answer any
	if record.OK {
		answer = o.Result.Text
		summary.Answer, _ = json.Marshal(o.Result.Text) // строка кодируется всегда
		if s.Schema != nil {
			summary.Answer = json.RawMessage(o.Result.Text)
			json.Unmarshal(summary.Answer, &answer) // ответ уже сверен со схемой
		}
	}
	r.mu.Lock()
	r.records = append(r.records, record)
	r.result.Stages = append(r.result.Stages, summary)
	r.mu.Unlock()
	if !record.OK {
		r.fail(&Failure{Stage: s.Name, Reason: record.Reason, Detail: detail})
		return nil, fmt.Errorf("этап %s сорван (%s): %s", s.Name, record.Reason, detail)
	}
	return answer, nil
}

// stage — этап агента на своём драйвере; detail — подробность срыва.
func (r *Run) stage(ctx context.Context, s *stage, responding func()) (*cost.Stage, string, error) {
	d, err := r.Driver(s.Driver)
	if err != nil {
		return nil, "", err
	}
	promptPath := r.Artifacts.SavePrompt(s.Name, s.Prompt)
	sandbox := &drivers.Sandbox{Readable: s.Readable}
	var env []string
	// Первый отказ команды; этап срывается им после агента.
	failed := make(chan error, 1)
	stop := func() {}
	if len(s.Allow) > 0 {
		// shortcut: сокет в каталоге команд этапа — путь до него не длиннее 107 байт только при коротком TMPDIR;
		// переносить, если упрётся.
		socket := filepath.Join(r.Bin, s.Name+".sock")
		// Клиенту в песочнице нужны его обёртка с сокетом и сам бинарь llamer.
		sandbox.Readable = append(append([]string{}, s.Readable...), r.Bin, r.ClientDir)
		env = []string{broker.SocketEnv + "=" + socket}
		if stop, err = r.Listen(socket, r.commands.handler(s, failed)); err != nil {
			return nil, "", err
		}
	}
	logx.Logf("этап «%s» пошёл: %s (%s/%s) → %s", s.Name, s.Driver, s.Model, s.Effort, promptPath)
	telemetry := agent.NewTelemetry(s.Name, nil, s.Skills, r.Cwd, &stream{r.Artifacts.OpenStream(s.Name), r.out, s.Name}, responding)
	outcome, err := d.Run(ctx, drivers.Args{Name: s.Name, Model: s.Model, Effort: s.Effort, Prompt: s.Prompt, Skills: s.Skills,
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
