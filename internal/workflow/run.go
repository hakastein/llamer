// Package workflow — прогон workflow: этапы по порядку конфига, ответ этапа
// доступен шаблонам следующих, события и результат — строками JSON в вывод.
package workflow

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/config"
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
	Allow     []string
	Cwd       string    // каталог контекста: рабочий каталог агента
	Out       io.Writer // события и результат строками JSON
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
	Type    string            `json:"type"`
	OK      bool              `json:"ok"`
	Run     string            `json:"run"`
	Failure *Failure          `json:"failure"`
	Stages  []Stage           `json:"stages"`
	Cost    agent.CostSummary `json:"cost"`
}

// out — вывод прогона. Вымарывание здесь, в одной точке: в строку попадают
// поток агента и его ответы.
type out struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *out) emit(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	line := redact.Secrets(string(data)) + "\n"
	o.mu.Lock()
	defer o.mu.Unlock()
	// Читатель вывода — сервис; закрытый им вывод прогон не останавливает: итог останется в result.json.
	io.WriteString(o.w, line)
	return line
}

// stream — поток этапа: в артефакты как есть и в вывод событием с именем этапа.
type stream struct {
	agent.Stream
	out   *out
	stage string
}

func (s *stream) Write(line []byte) {
	s.Stream.Write(line)
	s.out.emit(map[string]any{"type": "event", "stage": s.stage, "event": json.RawMessage(line)})
}

// Execute — этапы по порядку до первого сорванного. Прерванный прогон
// результата не выдаёт: возвращается ошибка контекста.
func (r *Run) Execute() (*Result, error) {
	o := &out{w: r.Out}
	result := &Result{Type: "result", OK: true, Run: r.Artifacts.Path, Stages: []Stage{}}
	answers := map[string]string{}
	var records []*agent.Record
	for _, s := range r.Workflow.Stages {
		record, detail, err := r.stage(s, answers, o)
		if err != nil {
			return nil, err
		}
		if err := r.Context.Err(); err != nil {
			return nil, err
		}
		records = append(records, record)
		stage := Stage{Name: s.Name, Driver: s.Driver, Model: record.Model, Effort: record.Effort, OK: record.OK, Answer: json.RawMessage("null"),
			Cost: record.Cost, Tokens: agent.TokensOf(record.Usage), ContextTokens: record.ContextTokens, Turns: record.Turns, DurationMs: record.DurationMs}
		if !record.OK {
			result.Stages = append(result.Stages, stage)
			result.OK, result.Failure = false, &Failure{Stage: s.Name, Reason: record.Reason, Detail: detail}
			break
		}
		answers[s.Name] = record.Result.Result
		stage.Answer = json.RawMessage(record.Result.Result)
		if s.Schema == nil {
			stage.Answer, _ = json.Marshal(record.Result.Result) // строка кодируется всегда
		}
		result.Stages = append(result.Stages, stage)
	}
	result.Cost = agent.Summarize(records)
	r.Artifacts.ReportCost(records, logx.SinceStart())
	if _, err := r.Artifacts.SaveText("result.json", o.emit(result)); err != nil {
		return nil, err
	}
	return result, nil
}

// stage — этап на своём драйвере; detail — подробность срыва.
func (r *Run) stage(s *config.WorkflowStage, answers map[string]string, o *out) (*agent.Record, string, error) {
	var prompt strings.Builder
	if err := s.Template.Execute(&prompt, config.PromptData{Answers: answers}); err != nil {
		return nil, "", err
	}
	promptPath := r.Artifacts.SavePrompt(s.Name, prompt.String())
	d := r.Drivers[s.Driver]
	launch, err := d.Launch(drivers.LaunchArgs{Stage: s.Name, Level: s.Level, Allow: r.Allow, Prompt: prompt.String(), Skills: s.Skills,
		Cwd: r.Cwd, Schema: s.SchemaPath})
	if err != nil {
		return nil, "", err
	}
	launch.Timeout = s.Timeout
	logx.Logf("этап «%s» пошёл: %s (%s/%s) → %s", s.Name, d.Name(), launch.Model, launch.Effort, promptPath)
	telemetry := agent.NewTelemetry(s.Name, nil, s.Skills, r.Cwd, &stream{r.Artifacts.OpenStream(s.Name), o, s.Name}, func() {}, agent.CommandProtocol{})
	record := agent.Spawn(r.Context, s.Name, launch, r.Cwd, telemetry)
	record.Key, record.Driver = s.Name, s.Driver
	record = telemetry.Finish(record)
	if !record.OK {
		detail := ""
		if record.Result != nil {
			detail = record.Result.Result
		}
		return record, detail, nil
	}
	if s.Schema == nil {
		return record, "", nil
	}
	if detail := check(s.Schema, record.Result.Result); detail != "" {
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
