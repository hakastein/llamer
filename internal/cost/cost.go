// Package cost — расход этапов прогона: таблица в лог, итог, cost.json и лимиты подписки драйверов.
package cost

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

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/logx"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Stage — этап прогона в отчёте о расходе: исход драйвера под именем этапа и вердикт того, кто этап запускал.
type Stage struct {
	Name, Driver string
	// OK и Reason — вердикт этапа: исход драйвера, если запустивший не сорвал этап по своим правилам.
	OK      bool
	Reason  string
	Outcome *agent.Outcome
}

func peakContext(stages []*Stage) int64 {
	var peak int64
	for _, s := range stages {
		peak = max(peak, s.Outcome.ContextTokens)
	}
	return peak
}

func usd(stages []*Stage) float64 {
	sum := 0.0
	for _, s := range stages {
		if s.Outcome.Cost != nil {
			sum += *s.Outcome.Cost
		}
	}
	return sum
}

var russian = message.NewPrinter(language.Russian)

// locale — число с разрядами по-русски: 1 234 567.
func locale(n int64) string { return russian.Sprint(n) }

func seconds(ms int64) string { return fmt.Sprint((ms + 500) / 1000) }

type stageRow struct{ name, model, effort, turns, tokens, context, cost string }

// rowOf — строка отчёта об этапе, одна на таблицу лога и markdown.
func rowOf(s *Stage) stageRow {
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
	return stageRow{name, o.Model, o.Effort, fmt.Sprint(o.Turns), approx + locale(o.Tokens()), locale(o.ContextTokens), agent.FormatCost(o)}
}

func Table(stages []*Stage, elapsedMs int64) string {
	rows := [][]string{{"Этап", "Модель", "Усилие", "Время", "Ходы", "Токены", "Размер контекста", "Цена"}}
	var tokens int64
	turns, worked, unknown := 0, int64(0), ""
	for _, s := range stages {
		r := rowOf(s)
		rows = append(rows, []string{r.name, r.model, r.effort, seconds(s.Outcome.DurationMs) + "с", r.turns, r.tokens, r.context, r.cost})
		tokens += s.Outcome.Tokens()
		turns += s.Outcome.Turns
		worked += s.Outcome.DurationMs
		if s.Outcome.Cost == nil {
			unknown = " + $?"
		}
	}
	rows = append(rows, []string{"ИТОГО", "", "", seconds(elapsedMs) + "с", fmt.Sprint(turns), locale(tokens), locale(peakContext(stages)),
		fmt.Sprintf("$%.2f", usd(stages)) + unknown})
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

func formatDuration(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return fmt.Sprintf("%d с", s)
	}
	return fmt.Sprintf("%d мин %d с", s/60, s%60)
}

// RunStats — токены и время по этапам: по ним видно, что разбор не был пустым.
func RunStats(stages []*Stage, elapsedMs int64) string {
	var tokens int64
	approx := ""
	var rows []string
	for _, s := range stages {
		tokens += s.Outcome.Tokens()
		if s.Outcome.UsageApprox {
			approx = "≈"
		}
		r := rowOf(s)
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |", r.name, r.model, r.effort, formatDuration(s.Outcome.DurationMs), r.turns, r.tokens, r.context))
	}
	lines := []string{
		fmt.Sprintf("Разбор занял %s, потрачено %s%s токенов.", formatDuration(elapsedMs), approx, locale(tokens)),
		"", "<details><summary>По этапам</summary>", "",
		"| Этап | Модель | Усилие | Время | Ходы | Токены | Размер контекста |",
		"|---|---|---|---|---|---|---|",
	}
	lines = append(append(lines, rows...), "", "</details>")
	return strings.Join(lines, "\n")
}

// Summary — итог цены прогона в результате и cost.json.
type Summary struct {
	USD           float64 `json:"usd" yaml:"usd"`
	USDKnown      bool    `json:"usdKnown" yaml:"usdKnown"`
	Turns         int     `json:"turns" yaml:"turns"`
	Tokens        int64   `json:"tokens" yaml:"tokens"`
	ContextTokens int64   `json:"contextTokens" yaml:"contextTokens"`
	DurationMs    int64   `json:"durationMs" yaml:"durationMs"`
}

func Summarize(stages []*Stage) Summary {
	c := Summary{USD: usd(stages), USDKnown: true, ContextTokens: peakContext(stages)}
	for _, s := range stages {
		if s.Outcome.Cost == nil || s.Outcome.UsageApprox {
			c.USDKnown = false
		}
		c.Turns += s.Outcome.Turns
		c.Tokens += s.Outcome.Tokens()
		c.DurationMs += s.Outcome.DurationMs
	}
	return c
}

func writeJSON(path string, stages []*Stage) error {
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
	data, err := json.MarshalIndent(map[string]any{"stages": items, "total": Summarize(stages)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o666)
}

// Report — расход прогона в лог и в cost.json каталога dir: таблица по этапам, лимиты драйверов и команды
// продолжения сессий.
func Report(dir string, stages []*Stage, elapsedMs int64) {
	if len(stages) == 0 {
		return
	}
	for _, line := range strings.Split(Table(stages, elapsedMs), "\n") {
		logx.Log(line)
	}
	limits := DriverLimits(stages)
	for _, driver := range slices.Sorted(maps.Keys(limits)) {
		logx.Log(formatLimits(driver, limits[driver]))
	}
	path := filepath.Join(dir, "cost.json")
	if err := writeJSON(path, stages); err != nil {
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

// FormatResetTime — время сброса лимита в UTC.
func FormatResetTime(resetsAt int64) string {
	return time.Unix(resetsAt, 0).UTC().Format("2006-01-02 15:04") + " UTC"
}

// DriverLimits — последние сведения о лимитах каждого драйвера за прогон;
// драйвер, не приславший ни окна, ни отказа, не попадает.
// shortcut: «последние» — по порядку этапов, а не по времени события: у
// параллельных этапов одной волны расходятся на минуты — брать время события, если это станет важно.
func DriverLimits(stages []*Stage) map[string]*agent.Limits {
	out := map[string]*agent.Limits{}
	for _, s := range stages {
		if l := s.Outcome.Limits; l != nil && (len(l.Windows) > 0 || (l.Status != nil && *l.Status != "" && *l.Status != "allowed")) {
			out[s.Driver] = l
		}
	}
	return out
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
			part += ", сброс " + FormatResetTime(*w.ResetsAt)
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
