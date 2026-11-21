package workflow

import (
	"context"
	"encoding/json"
	"os"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/drivers"
)

// stub — агент llamer run --dry-run: этап проходит без модели, ответ — пустая строка или наименьшее значение по
// схеме этапа. Задание ложится в артефакты прогона, как у настоящего этапа.
type stub struct{}

func (stub) Run(_ context.Context, a drivers.Args, _ *agent.Telemetry) (*agent.Outcome, error) {
	text := ""
	if a.Schema != "" {
		data, err := os.ReadFile(a.Schema)
		if err != nil {
			return nil, err
		}
		var schema any
		if err := json.Unmarshal(data, &schema); err != nil {
			return nil, err
		}
		answer, _ := json.Marshal(sample(schema)) // значения из разобранного JSON кодируются всегда
		text = string(answer)
	}
	return &agent.Outcome{Model: a.Model, Effort: a.Effort, OK: true, Result: &agent.Result{Text: text}, Cost: new(float64)}, nil
}

// sample — наименьшее значение по JSON Schema: const или первое из enum, у объекта — обязательные свойства, у массива —
// minItems элементов. shortcut: pattern, format, $ref и сочетания схем не учитываются — ответ заглушки тогда не по
// схеме и этап срывается; разбирать их, когда такая схема понадобится workflow.
func sample(schema any) any {
	s, _ := schema.(map[string]any)
	if v, ok := s["const"]; ok {
		return v
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	kind := s["type"]
	if kinds, ok := kind.([]any); ok && len(kinds) > 0 {
		kind = kinds[0]
	}
	switch kind {
	case "object":
		properties, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]any)
		out := map[string]any{}
		for _, name := range required {
			key, _ := name.(string)
			out[key] = sample(properties[key])
		}
		return out
	case "array":
		n, _ := s["minItems"].(float64)
		out := []any{}
		for range int(n) {
			out = append(out, sample(s["items"]))
		}
		return out
	case "string":
		return ""
	case "integer", "number":
		if minimum, ok := s["minimum"].(float64); ok {
			return minimum
		}
		return 0
	case "boolean":
		return false
	}
	return nil
}
