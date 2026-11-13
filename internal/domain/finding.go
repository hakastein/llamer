// Package domain — находки и журнал прогона.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Refusal — отказ агенту: чинится исправленным вызовом, а не остановкой прогона.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }

// Refuse — отказ с текстом.
func Refuse(format string, args ...any) *Refusal { return &Refusal{fmt.Sprintf(format, args...)} }

var Severities = []string{"minor", "major", "critical"}

const MaxSuggestionSpan = 20

// Related — место, на которое опирается находка; строки не заданы — nil.
type Related struct {
	File    string `json:"file"`
	Line    *int   `json:"line"`
	LineEnd *int   `json:"lineEnd" yaml:"lineEnd"`
}

var relatedItem = regexp.MustCompile(`^([^:]+)(?::(\d+)(?:-(\d+))?)?$`)

// ParseRelated — места «путь», «путь:строка», «путь:строка-строка» через запятую; прочее отбрасывается.
func ParseRelated(s string) []Related {
	out := []Related{}
	for _, item := range strings.Split(s, ",") {
		m := relatedItem.FindStringSubmatch(strings.TrimSpace(item))
		if m == nil {
			continue
		}
		file := strings.TrimSpace(m[1])
		if strings.Contains(file, "..") || strings.HasPrefix(file, "/") {
			continue
		}
		r := Related{File: file}
		if m[2] != "" {
			line, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			end := line
			if m[3] != "" {
				if end, err = strconv.Atoi(m[3]); err != nil {
					continue
				}
			}
			r.Line, r.LineEnd = &line, &end
		}
		out = append(out, r)
	}
	return out
}

// Finding — находка: событие записи и то, что с ней сделали дедупликация и скептик.
type Finding struct {
	FindingEvent
	ID int

	// `refuted: false` одинаков у проверенной находки и у той, до которой
	// скептик не дошёл: эти два случая разводит Verdicted.
	Verdicted    bool
	Refuted      bool
	RefuteReason string
	// Помеченный повтор не публикуется, но из журнала не пропадает: иначе
	// перекос дедупликатора в сторону лишней резки не заметить.
	DuplicateOf     int
	DuplicateReason string
}

func (f *FindingEvent) At() string { return fmt.Sprintf("%s:%d-%d", f.File, f.Line, f.LineEnd) }

func (f *Finding) Key() string { return f.At() + "\n" + f.Text }

func (f *FindingEvent) Span() int { return f.LineEnd - f.Line + 1 }

func (f *Finding) Survives() bool { return !f.Refuted && f.DuplicateOf == 0 }

func (f *Finding) AwaitsVerdict() bool { return !f.Verdicted && f.DuplicateOf == 0 }

// Revise меняет только названные поля.
func (f *Finding) Revise(a *Amend) {
	if a.Text != nil {
		f.Text = *a.Text
	}
	if a.Severity != nil {
		f.Severity = *a.Severity
	}
	if a.SeverityReason != nil {
		f.SeverityReason = *a.SeverityReason
	}
	if a.Suggestion != nil {
		f.Suggestion = a.Suggestion
	}
	if a.Related != nil {
		f.Related = *a.Related
	}
}

func (f *Finding) ApplyVerdict(v *Verdict) {
	f.Verdicted = true
	f.Refuted = v.Refuted
	f.RefuteReason = v.Reason
	if v.Severity != nil && *v.Severity != "" {
		f.Severity = *v.Severity
		if v.SeverityReason != nil {
			f.SeverityReason = *v.SeverityReason
		}
	}
}

// Anchors — строки диффа, к которым привязывается находка.
type Anchors interface {
	Touches(file string, deleted bool) bool
	HasLine(file string, line int, deleted bool) bool
	LinesOf(file string, deleted bool) []int
}

func (f *FindingEvent) AssertAnchored(diff Anchors) error {
	if !diff.Touches(f.File, f.Deleted) {
		if f.Deleted {
			return Refuse("в файле %s эти изменения строк не удаляли — --deleted не к чему привязать", f.File)
		}
		return Refuse("файла %s нет среди изменений задачи", f.File)
	}
	for _, number := range []int{f.Line, f.LineEnd} {
		if diff.HasLine(f.File, number, f.Deleted) {
			continue
		}
		state, kind := "не изменена", "Изменённые"
		if f.Deleted {
			state, kind = "не удалена", "Удалённые"
		}
		lines := diff.LinesOf(f.File, f.Deleted)
		parts := make([]string, len(lines))
		for i, l := range lines {
			parts[i] = strconv.Itoa(l)
		}
		return Refuse("строка %d в %s %s этими изменениями — тред к ней не привязать. %s строки: %s",
			number, f.File, state, kind, strings.Join(parts, ", "))
	}
	if f.LineEnd < f.Line {
		return Refuse("--line-end меньше --line")
	}
	return nil
}

func (f *FindingEvent) AssertSuggestionApplies(fileLines []string, suggestion string) error {
	// Правка кнопкой заменяет строки новой версии; у удалённой строки их нет.
	if f.Deleted {
		return Refuse("--suggestion не привязать к удалённой строке: заменять нечего, опиши правку в --text")
	}
	if f.Span() > MaxSuggestionSpan {
		return Refuse("правка на %d строк слишком велика: предел %d. Правку такого размера объясняют текстом, а не кнопкой применения",
			f.Span(), MaxSuggestionSpan)
	}
	if f.LineEnd > len(fileLines) {
		return Refuse("строка %d за концом %s (в нём %d строк)", f.LineEnd, f.File, len(fileLines))
	}
	from := max(f.Line-1, 0)
	to := max(f.LineEnd, from)
	if strings.Join(fileLines[from:to], "\n") == suggestion {
		return Refuse("правка совпадает с текущим кодом — заменять нечего. Проверь --line и --line-end")
	}
	return nil
}
