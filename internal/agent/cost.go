package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Usage — токены модели за этап, как в `modelUsage` итога claude.
type Usage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// Window — окно лимита подписки: использовано в процентах, сброс — unix-секунды.
type Window struct {
	UsedPercent float64 `json:"usedPercent" yaml:"usedPercent"`
	ResetsAt    *int64  `json:"resetsAt" yaml:"resetsAt"`
}

// Limits — лимиты подписки драйвера: статус, причина отказа и окна по имени.
type Limits struct {
	Status  *string           `json:"status" yaml:"status"`
	Reason  *string           `json:"reason" yaml:"reason"`
	Windows map[string]Window `json:"windows" yaml:"windows"`
}

// Record — запись отработавшего этапа: цена, токены, длительность, исход.
type Record struct {
	Name, Model, Effort string
	OK, TimedOut        bool
	ExitCode            *int
	Signal              string
	Result              *Event // итоговое событие result или nil
	DurationMs          int64
	Cost                *float64 // nil — драйвер цены не назвал: в отчёте это `$?`, а не $0
	Usage               map[string]Usage
	UsageApprox         bool
	ContextTokens       int64
	Requests            []json.RawMessage // usage каждого запроса — у драйвера, что считает их сам (codex)
	Limits              *Limits
	Turns               int
	SessionID           string

	Key, Driver, Resume string
	Reason              string
	RateLimit           json.RawMessage // rate_limit_info потока как есть
	LostWrites          int
}

// TokensOf — сумма токенов usage по моделям.
func TokensOf(usage map[string]Usage) int64 {
	var total int64
	for _, u := range usage {
		total += u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	return total
}

func peakContext(stages []*Record) int64 {
	var peak int64
	for _, s := range stages {
		peak = max(peak, s.ContextTokens)
	}
	return peak
}

func TotalCost(stages []*Record) float64 {
	total := 0.0
	for _, s := range stages {
		if s.Cost != nil {
			total += *s.Cost
		}
	}
	return total
}

// FormatCost — цена этапа. Токены, восстановленные из потока убитого этапа, —
// оценка снизу; цену, которую драйвер не назвал, не выдумываем.
func FormatCost(s *Record) string {
	if s.UsageApprox || s.Cost == nil {
		return "$?"
	}
	return fmt.Sprintf("$%.2f", *s.Cost)
}

var russian = message.NewPrinter(language.Russian)

// locale — число с разрядами по-русски: 1 234 567.
func locale(n int64) string { return russian.Sprint(n) }

func seconds(ms int64) string { return fmt.Sprint((ms + 500) / 1000) }

type stageRow struct{ name, model, effort, turns, tokens, context, cost string }

// rowOf — строка отчёта об этапе, одна на таблицу лога и markdown.
func rowOf(s *Record) stageRow {
	name := s.Name
	if !s.OK {
		reason := s.Reason
		if reason == "" {
			reason = "не выполнен"
		}
		name = s.Name + " (" + reason + ")"
	}
	approx := ""
	if s.UsageApprox {
		approx = "≈"
	}
	return stageRow{name, s.Model, s.Effort, fmt.Sprint(s.Turns), approx + locale(TokensOf(s.Usage)), locale(s.ContextTokens), FormatCost(s)}
}

func FormatCostTable(stages []*Record, elapsedMs int64) string {
	rows := [][]string{{"Этап", "Модель", "Усилие", "Время", "Ходы", "Токены", "Размер контекста", "Цена"}}
	var tokens int64
	turns, worked, unknown := 0, int64(0), ""
	for _, s := range stages {
		r := rowOf(s)
		rows = append(rows, []string{r.name, r.model, r.effort, seconds(s.DurationMs) + "с", r.turns, r.tokens, r.context, r.cost})
		tokens += TokensOf(s.Usage)
		turns += s.Turns
		worked += s.DurationMs
		if s.Cost == nil {
			unknown = " + $?"
		}
	}
	rows = append(rows, []string{"ИТОГО", "", "", seconds(elapsedMs) + "с", fmt.Sprint(turns), locale(tokens), locale(peakContext(stages)),
		fmt.Sprintf("$%.2f", TotalCost(stages)) + unknown})
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

// FormatRunStats — токены и время по этапам: по ним видно, что разбор не был пустым.
func FormatRunStats(stages []*Record, elapsedMs int64) string {
	var tokens int64
	approx := ""
	var rows []string
	for _, s := range stages {
		tokens += TokensOf(s.Usage)
		if s.UsageApprox {
			approx = "≈"
		}
		r := rowOf(s)
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |", r.name, r.model, r.effort, formatDuration(s.DurationMs), r.turns, r.tokens, r.context))
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

// CostSummary — итог цены прогона в результате и cost.json.
type CostSummary struct {
	USD           float64 `json:"usd" yaml:"usd"`
	USDKnown      bool    `json:"usdKnown" yaml:"usdKnown"`
	Turns         int     `json:"turns" yaml:"turns"`
	Tokens        int64   `json:"tokens" yaml:"tokens"`
	ContextTokens int64   `json:"contextTokens" yaml:"contextTokens"`
	DurationMs    int64   `json:"durationMs" yaml:"durationMs"`
}

func Summarize(stages []*Record) CostSummary {
	c := CostSummary{USD: TotalCost(stages), USDKnown: true, ContextTokens: peakContext(stages)}
	for _, s := range stages {
		if s.Cost == nil || s.UsageApprox {
			c.USDKnown = false
		}
		c.Turns += s.Turns
		c.Tokens += TokensOf(s.Usage)
		c.DurationMs += s.DurationMs
	}
	return c
}

func WriteCostJSON(path string, stages []*Record) error {
	type entry struct {
		Name          string             `json:"name"`
		Model         string             `json:"model"`
		Effort        *string            `json:"effort"`
		DurationMs    int64              `json:"durationMs"`
		Cost          *float64           `json:"cost"`
		Turns         int                `json:"turns"`
		Tokens        int64              `json:"tokens"`
		ContextTokens int64              `json:"contextTokens"`
		Requests      *[]json.RawMessage `json:"requests,omitempty"` // usage запросов как в журнале сессии codex
		OK            bool               `json:"ok"`
	}
	items := make([]entry, len(stages))
	for i, s := range stages {
		items[i] = entry{Name: s.Name, Model: s.Model, DurationMs: s.DurationMs, Cost: s.Cost, Turns: s.Turns,
			Tokens: TokensOf(s.Usage), ContextTokens: s.ContextTokens, OK: s.OK}
		if s.Effort != "" {
			items[i].Effort = &s.Effort
		}
		if s.Requests != nil {
			items[i].Requests = &s.Requests
		}
	}
	data, err := json.MarshalIndent(map[string]any{"stages": items, "total": Summarize(stages)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o666)
}

var windowTitles = map[string]string{"five_hour": "5 часов", "seven_day": "неделя"}

var statusTitles = map[string]string{"allowed_warning": "на исходе", "rejected": "исчерпан"}

// FormatResetTime — время сброса лимита в UTC.
func FormatResetTime(resetsAt int64) string {
	return time.Unix(resetsAt, 0).UTC().Format("2006-01-02 15:04") + " UTC"
}

// rateLimitInfo — `rate_limit_info` события claude: окна — в `unifiedWindows`
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

// ResetsAt — сброс лимита из `rate_limit_info`; nil — не назван.
func ResetsAt(info json.RawMessage) *int64 {
	var r rateLimitInfo
	if json.Unmarshal(info, &r) != nil || r.ResetsAt == nil {
		return nil
	}
	at := int64(*r.ResetsAt)
	return &at
}

// LimitsOf — лимиты подписки из `rate_limit_info` события claude.
func LimitsOf(raw json.RawMessage) *Limits {
	var info rateLimitInfo
	if len(raw) == 0 || json.Unmarshal(raw, &info) != nil {
		return nil
	}
	windows := info.UnifiedWindows
	if windows == nil {
		name := info.RateLimitType
		if name == "" {
			name = "window"
		}
		windows = map[string]rateLimitWindow{name: info.rateLimitWindow}
	}
	out := &Limits{Status: info.Status, Reason: info.ErrorCode, Windows: map[string]Window{}}
	for name, w := range windows {
		if w.Utilization == nil {
			continue
		}
		window := Window{UsedPercent: math.Round(*w.Utilization * 100)}
		if w.ResetsAt != nil {
			at := int64(*w.ResetsAt)
			window.ResetsAt = &at
		}
		out.Windows[name] = window
	}
	return out
}

// DriverLimits — последние сведения о лимитах каждого драйвера за прогон;
// драйвер, не приславший ни окна, ни отказа, не попадает.
// shortcut: «последние» — по порядку этапов, а не по времени события: у
// параллельных этапов одной волны расходятся на минуты — брать время события, если это станет важно.
func DriverLimits(records []*Record) map[string]*Limits {
	out := map[string]*Limits{}
	for _, r := range records {
		if l := r.Limits; l != nil && (len(l.Windows) > 0 || (l.Status != nil && *l.Status != "" && *l.Status != "allowed")) {
			out[r.Driver] = l
		}
	}
	return out
}

func FormatLimits(driver string, l *Limits) string {
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
