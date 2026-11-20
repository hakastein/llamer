// Package agent — процесс этапа и его поток в общей форме: запуск процесса, события, исход и телеметрия.
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
)

// Event — событие потока этапа в общей форме: драйвер переводит в неё свой поток.
type Event struct {
	Raw []byte // строка события, как она ложится в артефакты
	// Kind — тип события у драйвера: его называет сообщение о молчании этапа.
	Kind string
	// Failed — драйвер пометил событие ошибкой: лог показывает его строку целиком.
	Failed  bool
	Session *Session // начало сессии
	Note    string   // служебное событие драйвера одной строкой
	Stderr  string   // вывод мимо потока: stderr процесса или ошибка драйвера
	// Unparsed — строка вывода, которую драйвер не разобрал.
	Unparsed string
	// Start — начало ответа модели: с него кэш промпта, записанный запросом,
	// доступен соседним запросам. Ход модели — тоже её ответ.
	Start     bool
	Turn      *Turn      // ход модели
	Replies   []Reply    // ответы инструментов
	Result    *Result    // итог этапа
	RateLimit *RateLimit // сведения о лимитах подписки
}

// Session — начало сессии этапа.
type Session struct {
	ID    string
	Model string   // пусто — драйвер модель не назвал
	Tools int      // инструментов у сессии
	MCP   []string // серверы MCP: «имя=статус»
}

// Turn — ход модели: текст, размышления и вызовы инструментов по порядку.
type Turn struct {
	MessageID string // пусто — драйвер ходы не нумерует
	Usage     *Usage // токены запроса хода; nil — не названы
	Blocks    []Block
}

// Block — часть хода: текст, размышление или вызов инструмента.
type Block struct {
	Text     string
	Thinking string
	Call     *Call
}

// Call — вызов инструмента.
type Call struct {
	ID    string
	Name  string
	Input map[string]any // аргументы, как их показывает лог
	// Command — команда оболочки; File — файл, который вызов читает; Skill — скилл,
	// который он грузит. Пусто — вызов не такой.
	Command, File, Skill string
}

// Reply — ответ инструмента на вызов CallID.
type Reply struct {
	CallID  string
	Text    string
	IsError bool
}

// Result — итог этапа.
type Result struct {
	Text      string // ответ или текст ошибки
	IsError   bool
	APIStatus *int             // код ответа API, на котором этап сорвался; nil — не назван
	Cost      *float64         // nil — драйвер цены не назвал
	Usage     map[string]Usage // токены по моделям; nil — не названы
}

// RateLimit — сведения драйвера о лимитах подписки.
type RateLimit struct {
	Raw      json.RawMessage // как их прислал драйвер: так они ложатся в журнал срыва
	Limits   *Limits         // nil — не разобраны
	ResetsAt *int64          // сброс лимита; nil — не назван
}

const (
	grey   = "\x1b[90m"
	cyan   = "\x1b[36m"
	yellow = "\x1b[33m"
	reset  = "\x1b[0m"
)

func paint(color, text string, colors bool) string {
	if colors {
		return color + text + reset
	}
	return text
}

// Shorten — текст в одну строку не длиннее limit символов.
func Shorten(text string, limit int) string {
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

func sessionLine(s *Session) string {
	model := s.Model
	if model == "" {
		model = "модель не названа"
	}
	line := fmt.Sprintf("сессия: %s, инструментов %d", model, s.Tools)
	if len(s.MCP) > 0 {
		return line + ", MCP " + strings.Join(s.MCP, " ")
	}
	return line + ", MCP нет"
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
	if e.Failed {
		return prefix + paint(yellow, "ошибка от драйвера: "+Shorten(string(e.Raw), 1500), colors), true
	}
	var lines []string
	if e.Session != nil {
		lines = append(lines, prefix+paint(grey, sessionLine(e.Session), colors))
	}
	if e.Note != "" {
		lines = append(lines, prefix+paint(grey, e.Note, colors))
	}
	if text := strings.TrimSpace(e.Stderr); text != "" {
		lines = append(lines, prefix+paint(yellow, "stderr: "+text, colors))
	}
	if strings.TrimSpace(e.Unparsed) != "" {
		lines = append(lines, prefix+paint(yellow, "вывод не разобран: "+Shorten(e.Unparsed, 500), colors))
	}
	if e.Turn != nil {
		for _, b := range e.Turn.Blocks {
			switch {
			case b.Call != nil:
				lines = append(lines, prefix+paint(cyan, "→ "+b.Call.Name+": "+describeCall(b.Call.Input, cwd), colors))
			case strings.TrimSpace(b.Text) != "":
				lines = append(lines, prefix+strings.TrimSpace(b.Text))
			case strings.TrimSpace(b.Thinking) != "":
				lines = append(lines, prefix+paint(grey, "размышление: "+Shorten(b.Thinking, 400), colors))
			}
		}
	}
	for _, r := range e.Replies {
		mark := "←"
		if r.IsError {
			mark = paint(yellow, "← ошибка", colors)
		}
		lines = append(lines, prefix+mark+" "+Shorten(r.Text, 200))
	}
	return strings.Join(lines, "\n"), len(lines) > 0
}

// Stats — статистика этапа по его событиям.
type Stats struct {
	cwd        string
	skills     map[string]bool
	commands   []string
	callNames  []string
	calls      map[string]int
	files      map[string]bool
	errors     int
	messages   map[string]bool
	looseTurns int
}

// StatsFrom — статистика событий этапа.
func StatsFrom(events []*Event, cwd string, skills, commands []string) *Stats {
	s := &Stats{cwd: cwd, skills: map[string]bool{}, commands: commands, calls: map[string]int{}, files: map[string]bool{},
		messages: map[string]bool{}}
	for _, name := range skills {
		s.skills[name] = true
	}
	for _, e := range events {
		s.add(e)
	}
	return s
}

func (s *Stats) add(e *Event) {
	if e.Turn != nil {
		if e.Turn.MessageID != "" {
			s.messages[e.Turn.MessageID] = true
		} else {
			s.looseTurns++
		}
		for _, b := range e.Turn.Blocks {
			if b.Call != nil {
				s.addCall(b.Call)
			}
		}
	}
	for _, r := range e.Replies {
		if r.IsError {
			s.errors++
		}
	}
}

func (s *Stats) addCall(c *Call) {
	if _, ok := s.calls[c.Name]; !ok {
		s.callNames = append(s.callNames, c.Name)
	}
	s.calls[c.Name]++
	if c.File != "" {
		s.files[RelativeToCwd(c.File, s.cwd)] = true
	}
	if c.Command != "" {
		for _, f := range FilesIn(c.Command, s.cwd) {
			s.files[f] = true
		}
	}
	if c.Skill != "" {
		s.skills[c.Skill] = true
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
