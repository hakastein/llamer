package agent

import "encoding/json"

// Usage — токены модели: вход без кэша, выход, чтение и запись кэша.
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

// Outcome — исход этапа на драйвере: процесс, итог, расход и лимиты. Драйвер
// отдаёт его готовым; что этап сорван по своим правилам, решает тот, кто его запускал.
type Outcome struct {
	Model, Effort string
	// OK — процесс вышел с нулём, итог без ошибки, этап не прерван; иначе Reason — причина срыва.
	OK       bool
	Reason   string
	ExitCode *int // nil — процесс не запустился или убит сигналом
	Signal   string
	Result   *Result  // итог этапа или nil
	Events   []*Event // события драйвера по порядку
	// DurationMs — от запуска процесса до его выхода.
	DurationMs    int64
	Cost          *float64 // nil — цена неизвестна: в отчёте это `$?`, а не $0
	Usage         map[string]Usage
	UsageApprox   bool // токены собраны из ходов потока: итога с ними не было
	ContextTokens int64
	Requests      []json.RawMessage // usage каждого запроса — у драйвера, что считает их сам
	Limits        *Limits
	Turns         int
	RateLimit     json.RawMessage // сведения о лимитах из потока драйвера как есть
	ResetsAt      *int64          // сброс лимита из тех же сведений; nil — не назван
	// Resume — команда продолжения сессии, к которой дописывают вопрос; пусто — сессия не сохранена.
	Resume string
}

// Tokens — сумма токенов по моделям.
func (o *Outcome) Tokens() int64 {
	var total int64
	for _, u := range o.Usage {
		total += u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	return total
}
