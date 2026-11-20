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

// relativeToCwd — путь от cwd, если он внутри него.
func relativeToCwd(path, cwd string) string {
	if cwd != "" && strings.HasPrefix(path, cwd) {
		return strings.TrimPrefix(path[len(cwd):], "/")
	}
	return path
}

var commandTokens = regexp.MustCompile(`(?:\s|['"|;&()])+`)

// filesIn — файлы, названные в команде: этап читает и `cat`, и `sed -n`, и
// `grep`, поэтому список прочитанного, собранный по одному Read, показывает
// меньше, чем было. Ошибка файловой системы, кроме несуществующего пути, — сбой.
func filesIn(command, cwd string) []string {
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
			found = append(found, relativeToCwd(path, cwd))
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
			text = relativeToCwd(text, cwd)
		} else {
			text = jsonText(input[k])
		}
		parts = append(parts, k+"="+text)
	}
	return strings.Join(parts, " ")
}

// formatEvent — строка лога о событии потока; ok=false — показывать нечего.
func formatEvent(e *Event, colors bool, cwd, agent string) (string, bool) {
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

// stats — статистика этапа по его событиям.
type stats struct {
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

// statsFrom — статистика событий этапа.
func statsFrom(events []*Event, cwd string, skills, commands []string) *stats {
	s := &stats{cwd: cwd, skills: map[string]bool{}, commands: commands, calls: map[string]int{}, files: map[string]bool{},
		messages: map[string]bool{}}
	for _, name := range skills {
		s.skills[name] = true
	}
	for _, e := range events {
		s.add(e)
	}
	return s
}

func (s *stats) add(e *Event) {
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

func (s *stats) addCall(c *Call) {
	if _, ok := s.calls[c.Name]; !ok {
		s.callNames = append(s.callNames, c.Name)
	}
	s.calls[c.Name]++
	if c.File != "" {
		s.files[relativeToCwd(c.File, s.cwd)] = true
	}
	if c.Command != "" {
		for _, f := range filesIn(c.Command, s.cwd) {
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

func (s *stats) Lines() []string {
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
