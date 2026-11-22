package report

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/cost"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var russian = message.NewPrinter(language.Russian)

// number — число с разрядами по-русски: 1 234 567.
func number(n int64) string { return russian.Sprint(n) }

func seconds(ms int64) string { return fmt.Sprint((ms + 500) / 1000) }

// row — этап строкой отчёта: имя с причиной срыва, модель, усилие, ходы, токены, размер контекста и цена.
type row struct{ Name, Model, Effort, Turns, Tokens, Context, Cost string }

// rowOf — строка отчёта об этапе, одна на таблицу лога и отчёты.
func rowOf(s *cost.Stage) row {
	o := s.Outcome
	name := s.Name
	if !s.OK {
		reason := s.Reason
		if reason == "" {
			reason = "не выполнен"
		}
		name = s.Name + " (" + reason + ")"
	}
	approx := ""
	if o.UsageApprox {
		approx = "≈"
	}
	return row{name, o.Model, o.Effort, fmt.Sprint(o.Turns), approx + number(o.Tokens()), number(o.ContextTokens), agent.FormatCost(o)}
}

// table — таблица расхода по этапам и итог прогона длительностью elapsedMs.
func table(stages []*cost.Stage, elapsedMs int64) string {
	rows := [][]string{{"Этап", "Модель", "Усилие", "Время", "Ходы", "Токены", "Размер контекста", "Цена"}}
	var worked int64
	unknown := ""
	for _, s := range stages {
		r := rowOf(s)
		rows = append(rows, []string{r.Name, r.Model, r.Effort, seconds(s.Outcome.DurationMs) + "с", r.Turns, r.Tokens, r.Context, r.Cost})
		worked += s.Outcome.DurationMs
		if s.Outcome.Cost == nil {
			unknown = " + $?"
		}
	}
	total := cost.Summarize(stages)
	rows = append(rows, []string{"ИТОГО", "", "", seconds(elapsedMs) + "с", fmt.Sprint(total.Turns), number(total.Tokens), number(total.ContextTokens),
		fmt.Sprintf("$%.2f", total.USD) + unknown})
	if worked > elapsedMs {
		rows = append(rows, []string{"  из них параллельно", "", "", seconds(worked-elapsedMs) + "с", "", "", "", ""})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row))
		for j, cell := range row {
			cells[j] = cell + strings.Repeat(" ", widths[j]-utf8.RuneCountInString(cell))
		}
		lines[i] = strings.Join(cells, "  ")
	}
	return strings.Join(lines, "\n")
}

func writeCostJSON(path string, stages []*cost.Stage) error {
	type entry struct {
		Name          string             `json:"name"`
		Model         string             `json:"model"`
		Effort        *string            `json:"effort"`
		DurationMs    int64              `json:"durationMs"`
		Cost          *float64           `json:"cost"`
		Turns         int                `json:"turns"`
		Tokens        int64              `json:"tokens"`
		ContextTokens int64              `json:"contextTokens"`
		Requests      *[]json.RawMessage `json:"requests,omitempty"` // usage запросов как в журнале сессии драйвера
		OK            bool               `json:"ok"`
	}
	items := make([]entry, len(stages))
	for i, s := range stages {
		o := s.Outcome
		items[i] = entry{Name: s.Name, Model: o.Model, DurationMs: o.DurationMs, Cost: o.Cost, Turns: o.Turns,
			Tokens: o.Tokens(), ContextTokens: o.ContextTokens, OK: s.OK}
		if o.Effort != "" {
			items[i].Effort = &o.Effort
		}
		if o.Requests != nil {
			items[i].Requests = &o.Requests
		}
	}
	data, err := json.MarshalIndent(map[string]any{"stages": items, "total": cost.Summarize(stages)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o666)
}

// LogCost — расход прогона в лог и в cost.json каталога dir: таблица по этапам, лимиты драйверов и команды
// продолжения сессий.
func LogCost(dir string, stages []*cost.Stage, elapsedMs int64) {
	if len(stages) == 0 {
		return
	}
	for _, line := range strings.Split(table(stages, elapsedMs), "\n") {
		logx.Log(line)
	}
	limits := cost.DriverLimits(stages)
	for _, driver := range slices.Sorted(maps.Keys(limits)) {
		logx.Log(formatLimits(driver, limits[driver]))
	}
	path := filepath.Join(dir, "cost.json")
	if err := writeCostJSON(path, stages); err != nil {
		panic(err)
	}
	logx.Log("стоимость по этапам: " + path)
	for _, s := range stages {
		if s.Outcome.Resume != "" {
			logx.Logf("продолжить сессию «%s» вопросом: %s '<вопрос>'", s.Name, s.Outcome.Resume)
		}
	}
}

var windowTitles = map[string]string{"five_hour": "5 часов", "seven_day": "неделя"}

var statusTitles = map[string]string{"allowed_warning": "на исходе", "rejected": "исчерпан"}

// formatResetTime — время сброса лимита в UTC.
func formatResetTime(resetsAt int64) string {
	return time.Unix(resetsAt, 0).UTC().Format("2006-01-02 15:04") + " UTC"
}

func formatLimits(driver string, l *agent.Limits) string {
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(l.Windows)) {
		w := l.Windows[name]
		title, ok := windowTitles[name]
		if !ok {
			title = name
		}
		part := fmt.Sprintf("%s — использовано %g%%, осталось %g%%", title, w.UsedPercent, 100-w.UsedPercent)
		if w.ResetsAt != nil && *w.ResetsAt != 0 {
			part += ", сброс " + formatResetTime(*w.ResetsAt)
		}
		parts = append(parts, part)
	}
	if l.Status != nil && *l.Status != "" && *l.Status != "allowed" {
		title, ok := statusTitles[*l.Status]
		if !ok {
			title = *l.Status
		}
		if l.Reason != nil && *l.Reason != "" {
			title += " (" + *l.Reason + ")"
		}
		parts = append([]string{title}, parts...)
	}
	return "лимиты " + driver + ": " + strings.Join(parts, "; ")
}
