// Package agent — процесс этапа: запуск драйвера, разбор его потока, цена и лимиты.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/hakastein/llamer/internal/harness"
)

// Event — событие потока этапа в форме claude: поля, которые читает llamer, и
// строка как пришла — она ложится в артефакты.
type Event struct {
	Raw        json.RawMessage `json:"-"`
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	SessionID  string          `json:"session_id"`
	Model      string          `json:"model"`
	Tools      []any           `json:"tools"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	Status   string `json:"status"`
	HookName string `json:"hook_name"`
	// Message — у system строка, у assistant и user — сообщение с блоками.
	RawMessage json.RawMessage `json:"message"`
	Message    Message         `json:"-"`
	Text       string          `json:"text"`
	Marks
	Result       string           `json:"result"`
	TotalCostUSD *float64         `json:"total_cost_usd"`
	ModelUsage   map[string]Usage `json:"modelUsage"`
	Inner        struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	} `json:"event"`
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
}

// Marks — признаки ошибки: у события и у его сообщения.
type Marks struct {
	IsError           bool            `json:"is_error"`
	Error             json.RawMessage `json:"error"`
	IsAPIErrorMessage bool            `json:"is_api_error_message"`
	APIErrorStatus    *int            `json:"api_error_status"`
}

func (m Marks) marked() bool {
	return m.IsError || (len(m.Error) > 0 && string(m.Error) != "null") || m.IsAPIErrorMessage || m.APIErrorStatus != nil
}

type Message struct {
	ID      string      `json:"id"`
	Content []Block     `json:"content"`
	Usage   *TokenUsage `json:"usage"`
	Marks
}

type TokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// Block — блок сообщения: текст, размышление, вызов инструмента или его ответ.
type Block struct {
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

// ParseEvent — событие строки потока.
func ParseEvent(raw []byte) (*Event, error) {
	e := &Event{Raw: raw}
	if err := json.Unmarshal(raw, e); err != nil {
		return nil, err
	}
	if len(e.RawMessage) > 0 && e.RawMessage[0] == '{' {
		if err := json.Unmarshal(e.RawMessage, &e.Message); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// ownEvent — событие llamer о процессе этапа: вывод stderr или неразобранная строка.
func ownEvent(typ, name, text string) *Event {
	raw, _ := json.Marshal(map[string]string{"type": typ, "name": name, "text": text})
	return &Event{Raw: raw, Type: typ, Text: text}
}

// Вызов харнесса в команде Bash, в том числе после `;`, `&&`, `|` и внутри
// `bash -lc '…'`, в которую codex заворачивает команды: имя его команды.
var harnessCall = regexp.MustCompile(`(?:^|[;&|\n'"])\s*` + harness.Name + `\s+(\S+)`)

const (
	grey     = "\x1b[90m"
	cyan     = "\x1b[36m"
	yellow   = "\x1b[33m"
	reset    = "\x1b[0m"
	thinking = "размышляет…"
)

func paint(color, text string, colors bool) string {
	if colors {
		return color + text + reset
	}
	return text
}

// shorten — текст в одну строку не длиннее limit символов.
func shorten(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if r := []rune(flat); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return flat
}

func jsonText(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// RelativeToCwd — путь от cwd, если он внутри него.
func RelativeToCwd(path, cwd string) string {
	if cwd != "" && strings.HasPrefix(path, cwd) {
		return strings.TrimPrefix(path[len(cwd):], "/")
	}
	return path
}

var commandTokens = regexp.MustCompile(`(?:\s|['"|;&()])+`)

// FilesIn — файлы, названные в команде: этап читает и `cat`, и `sed -n`, и
// `grep`, поэтому список прочитанного, собранный по одному Read, показывает
// меньше, чем было. Ошибка файловой системы, кроме несуществующего пути, — сбой.
func FilesIn(command, cwd string) []string {
	var found []string
	for _, token := range commandTokens.Split(command, -1) {
		if token == "" || strings.HasPrefix(token, "-") {
			continue
		}
		path := token
		if !filepath.IsAbs(token) {
			path = filepath.Join(cwd, token)
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			panic(err)
		}
		if info.Mode().IsRegular() {
			found = append(found, RelativeToCwd(path, cwd))
		}
	}
	return found
}

// toolResultText — блоки ответа не всегда текстовые: ToolSearch отвечает ссылками на инструменты.
func toolResultText(b Block) string {
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

func (e *Event) blocks(typ string) []Block {
	if e.Type != typ {
		return nil
	}
	return e.Message.Content
}

func formatSystemEvent(e *Event) (string, bool) {
	switch {
	case e.Subtype == "status" && e.Status == "requesting":
		return "", false
	case e.Subtype == "thinking_tokens":
		return thinking, true
	case e.Subtype == "init":
		model := e.Model
		if model == "" {
			model = "модель не названа"
		}
		var mcp []string
		for _, s := range e.MCPServers {
			mcp = append(mcp, s.Name+"="+s.Status)
		}
		line := fmt.Sprintf("сессия: %s, инструментов %d", model, len(e.Tools))
		if len(mcp) > 0 {
			return line + ", MCP " + strings.Join(mcp, " "), true
		}
		return line + ", MCP нет", true
	case e.Subtype == "status":
		if e.Status == "" {
			return "статус: " + shorten(string(e.Raw), 160), true
		}
		return "статус: " + e.Status, true
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
		line += ": " + shorten(detail, 80)
	}
	return line, true
}

func errorDump(e *Event) string {
	if strings.Contains(e.Subtype, "error") || e.marked() || e.Message.marked() {
		return "ошибка от драйвера: " + shorten(string(e.Raw), 1500)
	}
	return ""
}

func describeCall(input map[string]any, cwd string) string {
	var parts []string
	if command, ok := input["command"].(string); ok {
		parts = append(parts, command)
	}
	for _, k := range slices.Sorted(maps.Keys(input)) {
		if k == "command" {
			continue
		}
		text, ok := input[k].(string)
		if ok {
			text = RelativeToCwd(text, cwd)
		} else {
			text = jsonText(input[k])
		}
		parts = append(parts, k+"="+text)
	}
	return strings.Join(parts, " ")
}

// FormatStreamEvent — строка лога о событии потока; ok=false — показывать нечего.
func FormatStreamEvent(e *Event, colors bool, cwd, agent string) (string, bool) {
	prefix := ""
	if agent != "" {
		prefix = paint(grey, "["+agent+"] ", colors)
	}
	if failure := errorDump(e); failure != "" {
		return prefix + paint(yellow, failure, colors), true
	}
	switch e.Type {
	case "system":
		line, ok := formatSystemEvent(e)
		if !ok {
			return "", false
		}
		return prefix + paint(grey, line, colors), true
	case "stage_stderr":
		if text := strings.TrimSpace(e.Text); text != "" {
			return prefix + paint(yellow, "stderr: "+text, colors), true
		}
		return "", false
	case "stage_raw":
		if strings.TrimSpace(e.Text) != "" {
			return prefix + paint(yellow, "вывод не разобран: "+shorten(e.Text, 500), colors), true
		}
		return "", false
	case "assistant":
		var lines []string
		for _, b := range e.blocks("assistant") {
			switch b.Type {
			case "text":
				if text := strings.TrimSpace(b.Text); text != "" {
					lines = append(lines, prefix+text)
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					lines = append(lines, prefix+paint(grey, "размышление: "+shorten(b.Thinking, 400), colors))
				}
			case "tool_use":
				lines = append(lines, prefix+paint(cyan, "→ "+b.Name+": "+describeCall(b.Input, cwd), colors))
			}
		}
		return strings.Join(lines, "\n"), len(lines) > 0
	case "user":
		var lines []string
		for _, b := range e.blocks("user") {
			if b.Type != "tool_result" {
				continue
			}
			mark := "←"
			if b.IsError {
				mark = paint(yellow, "← ошибка", colors)
			}
			lines = append(lines, prefix+mark+" "+shorten(toolResultText(b), 200))
		}
		return strings.Join(lines, "\n"), len(lines) > 0
	case "stream_event":
		// Пустой thinking-блок — единственный признак, что модель думает, когда
		// содержимое размышлений не возвращается.
		if e.Inner.Type == "content_block_start" && e.Inner.ContentBlock.Type == "thinking" {
			return prefix + paint(grey, thinking, colors), true
		}
	}
	return "", false
}

// Stats — статистика этапа по его событиям.
type Stats struct {
	cwd           string
	skills        map[string]bool
	commands      []string
	callNames     []string
	calls         map[string]int
	files         map[string]bool
	errors        int
	writes        []string
	harnessWrites map[string]bool
	LostWrites    int
	messages      map[string]bool
	looseTurns    int
}

// StatsFrom — статистика событий; writes — команды харнесса, которые пишут в
// журнал: только их сорванный вызов теряет запись.
func StatsFrom(events []*Event, cwd string, skills, commands, writes []string) *Stats {
	s := &Stats{cwd: cwd, skills: map[string]bool{}, commands: commands, calls: map[string]int{}, files: map[string]bool{},
		writes: writes, harnessWrites: map[string]bool{}, messages: map[string]bool{}}
	for _, name := range skills {
		s.skills[name] = true
	}
	for _, e := range events {
		s.add(e)
	}
	return s
}

var refusalLine = regexp.MustCompile(`(?m)^отказ:`)

func (s *Stats) add(e *Event) {
	if e.Type == "assistant" {
		if e.Message.ID != "" {
			s.messages[e.Message.ID] = true
		} else {
			s.looseTurns++
		}
	}
	for _, b := range e.blocks("assistant") {
		if b.Type != "tool_use" {
			continue
		}
		if _, ok := s.calls[b.Name]; !ok {
			s.callNames = append(s.callNames, b.Name)
		}
		s.calls[b.Name]++
		if path, ok := b.Input["file_path"].(string); b.Name == "Read" && ok && path != "" {
			s.files[RelativeToCwd(path, s.cwd)] = true
		}
		if command, ok := b.Input["command"].(string); b.Name == "Bash" && ok && command != "" {
			for _, m := range harnessCall.FindAllStringSubmatch(command, -1) {
				if slices.Contains(s.writes, m[1]) {
					s.harnessWrites[b.ID] = true
				}
			}
			for _, f := range FilesIn(command, s.cwd) {
				s.files[f] = true
			}
		}
		if skill, ok := b.Input["skill"].(string); b.Name == "Skill" && ok && skill != "" {
			s.skills[skill] = true
		}
	}
	for _, b := range e.blocks("user") {
		if b.Type != "tool_result" || !b.IsError {
			continue
		}
		s.errors++
		// Отказ харнесса агент чинит повтором; вызов, не дошедший до харнесса
		// (права, песочница, сбой), уносит запись с собой.
		if s.harnessWrites[b.ToolUseID] && !refusalLine.MatchString(toolResultText(b)) {
			s.LostWrites++
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Stats) Lines() []string {
	var lines []string
	if len(s.callNames) == 0 {
		lines = append(lines, "агент не вызвал ни одного инструмента")
	} else {
		names := slices.Clone(s.callNames)
		sort.SliceStable(names, func(i, j int) bool { return s.calls[names[i]] > s.calls[names[j]] })
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = fmt.Sprintf("%s×%d", n, s.calls[n])
		}
		lines = append(lines, "инструменты: "+strings.Join(parts, ", "))
	}
	if s.errors > 0 {
		lines = append(lines, fmt.Sprintf("отказов: %d", s.errors))
	}
	if s.LostWrites > 0 {
		lines = append(lines, fmt.Sprintf("вызовов %s, не дошедших до журнала: %d", harness.Name, s.LostWrites))
	}
	if skills := sortedKeys(s.skills); len(skills) > 0 {
		lines = append(lines, "скиллы: "+strings.Join(skills, ", "))
	} else {
		lines = append(lines, "скиллов у этапа нет")
	}
	lines = append(lines, "команды: "+strings.Join(s.commands, ", "), fmt.Sprintf("ходов: %d", len(s.messages)+s.looseTurns))
	if files := sortedKeys(s.files); len(files) > 0 {
		lines = append(lines, fmt.Sprintf("прочитано файлов: %d", len(files)))
		for _, f := range files {
			lines = append(lines, "  "+f)
		}
	}
	return lines
}
