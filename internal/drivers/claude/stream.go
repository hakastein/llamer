package claude

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/hakastein/llamer/internal/agent"
)

// event — событие потока `--output-format stream-json`: поля, которые читает llamer.
type event struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	SessionID  string `json:"session_id"`
	Model      string `json:"model"`
	Tools      []any  `json:"tools"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	Status   string `json:"status"`
	HookName string `json:"hook_name"`
	// Message — у system строка, у assistant и user — сообщение с блоками.
	RawMessage json.RawMessage `json:"message"`
	marks
	Result       string                `json:"result"`
	TotalCostUSD *float64              `json:"total_cost_usd"`
	ModelUsage   map[string]modelUsage `json:"modelUsage"`
	Inner        struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	} `json:"event"`
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
}

// marks — признаки ошибки: у события и у его сообщения.
type marks struct {
	IsError           bool            `json:"is_error"`
	Error             json.RawMessage `json:"error"`
	IsAPIErrorMessage bool            `json:"is_api_error_message"`
	APIErrorStatus    *int            `json:"api_error_status"`
}

func (m marks) marked() bool {
	return m.IsError || (len(m.Error) > 0 && string(m.Error) != "null") || m.IsAPIErrorMessage || m.APIErrorStatus != nil
}

type message struct {
	ID      string      `json:"id"`
	Content []block     `json:"content"`
	Usage   *tokenUsage `json:"usage"`
	marks
}

type tokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// modelUsage — токены модели за этап в `modelUsage` итога.
type modelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// block — блок сообщения: текст, размышление, вызов инструмента или его ответ.
type block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	IsError   bool           `json:"is_error"`
	// Content у tool_result бывает и строкой, и массивом блоков.
	Content json.RawMessage `json:"content"`
}

const thinking = "размышляет…"

// parse — событие строки потока в общей форме.
func parse(raw []byte) ([]*agent.Event, error) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	var m message
	if len(e.RawMessage) > 0 && e.RawMessage[0] == '{' {
		if err := json.Unmarshal(e.RawMessage, &m); err != nil {
			return nil, err
		}
	}
	out := &agent.Event{Raw: raw, Kind: e.Type, Failed: strings.Contains(e.Subtype, "error") || e.marked() || m.marked()}
	if e.Subtype != "" {
		out.Kind += "/" + e.Subtype
	}
	switch e.Type {
	case "system":
		if e.Subtype == "init" {
			out.Session = &agent.Session{ID: e.SessionID, Model: e.Model, Tools: len(e.Tools)}
			for _, s := range e.MCPServers {
				out.Session.MCP = append(out.Session.MCP, s.Name+"="+s.Status)
			}
		} else {
			out.Note = systemNote(e, raw)
		}
	case "stream_event":
		out.Start = e.Inner.Type == "message_start"
		// Пустой thinking-блок — единственный признак, что модель думает, когда
		// содержимое размышлений не возвращается.
		if e.Inner.Type == "content_block_start" && e.Inner.ContentBlock.Type == "thinking" {
			out.Note = thinking
		}
	case "assistant":
		out.Turn = &agent.Turn{MessageID: m.ID}
		if u := m.Usage; u != nil {
			out.Turn.Usage = &agent.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
				CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens}
		}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				out.Turn.Blocks = append(out.Turn.Blocks, agent.Block{Text: b.Text})
			case "thinking":
				out.Turn.Blocks = append(out.Turn.Blocks, agent.Block{Thinking: b.Thinking})
			case "tool_use":
				out.Turn.Blocks = append(out.Turn.Blocks, agent.Block{Call: callOf(b)})
			}
		}
	case "user":
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				out.Replies = append(out.Replies, agent.Reply{CallID: b.ToolUseID, Text: toolResultText(b), IsError: b.IsError})
			}
		}
	case "result":
		out.Result = &agent.Result{Text: e.Result, IsError: e.IsError, APIStatus: e.APIErrorStatus, Cost: e.TotalCostUSD}
		if e.ModelUsage != nil {
			out.Result.Usage = map[string]agent.Usage{}
			for model, u := range e.ModelUsage {
				out.Result.Usage[model] = agent.Usage(u)
			}
		}
	case "rate_limit_event":
		if len(e.RateLimitInfo) > 0 && string(e.RateLimitInfo) != "null" {
			out.RateLimit = rateLimitOf(e.RateLimitInfo)
		}
	}
	return []*agent.Event{out}, nil
}

// callOf — вызов инструмента: что он читает, запускает и грузит, — по инструментам Claude Code.
func callOf(b block) *agent.Call {
	c := &agent.Call{ID: b.ID, Name: b.Name, Input: b.Input}
	switch b.Name {
	case "Bash":
		c.Command, _ = b.Input["command"].(string)
	case "Read":
		c.File, _ = b.Input["file_path"].(string)
	case "Skill":
		c.Skill, _ = b.Input["skill"].(string)
	}
	return c
}

func systemNote(e event, raw []byte) string {
	switch {
	case e.Subtype == "status" && e.Status == "requesting":
		return ""
	case e.Subtype == "thinking_tokens":
		return thinking
	case e.Subtype == "status":
		if e.Status == "" {
			return "статус: " + agent.Shorten(string(raw), 160)
		}
		return "статус: " + e.Status
	}
	detail := e.HookName
	if detail == "" {
		json.Unmarshal(e.RawMessage, &detail) // у system сообщение — строка; иначе подробности нет
	}
	subtype := e.Subtype
	if subtype == "" {
		subtype = "без подтипа"
	}
	line := "system/" + subtype
	if detail != "" {
		line += ": " + agent.Shorten(detail, 80)
	}
	return line
}

// toolResultText — блоки ответа не всегда текстовые: ToolSearch отвечает ссылками на инструменты.
func toolResultText(b block) string {
	var text string
	if json.Unmarshal(b.Content, &text) == nil {
		return text
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ToolName string `json:"tool_name"`
	}
	if json.Unmarshal(b.Content, &parts) != nil {
		return string(b.Content)
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "tool_reference" && p.Text == "" {
			p.Text = p.ToolName
		}
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, " ")
}

// rateLimitInfo — `rate_limit_info` события: окна — в `unifiedWindows`
// живого потока; документация знает одно окно на верхнем уровне.
type rateLimitInfo struct {
	Status         *string                    `json:"status"`
	ErrorCode      *string                    `json:"errorCode"`
	RateLimitType  string                     `json:"rateLimitType"`
	UnifiedWindows map[string]rateLimitWindow `json:"unifiedWindows"`
	rateLimitWindow
}

type rateLimitWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *float64 `json:"resetsAt"`
}

// rateLimitOf — сведения о лимитах из `rate_limit_info`: лимиты и сброс разбираются, если разбирается JSON.
func rateLimitOf(raw json.RawMessage) *agent.RateLimit {
	out := &agent.RateLimit{Raw: raw}
	var info rateLimitInfo
	if json.Unmarshal(raw, &info) != nil {
		return out
	}
	if info.ResetsAt != nil {
		at := int64(*info.ResetsAt)
		out.ResetsAt = &at
	}
	windows := info.UnifiedWindows
	if windows == nil {
		name := info.RateLimitType
		if name == "" {
			name = "window"
		}
		windows = map[string]rateLimitWindow{name: info.rateLimitWindow}
	}
	out.Limits = &agent.Limits{Status: info.Status, Reason: info.ErrorCode, Windows: map[string]agent.Window{}}
	for name, w := range windows {
		if w.Utilization == nil {
			continue
		}
		window := agent.Window{UsedPercent: math.Round(*w.Utilization * 100)}
		if w.ResetsAt != nil {
			at := int64(*w.ResetsAt)
			window.ResetsAt = &at
		}
		out.Limits.Windows[name] = window
	}
	return out
}
