// Package agent — процесс этапа и его поток в общей форме: запуск процесса, события, исход и телеметрия.
package agent

import "encoding/json"

// Event — событие потока этапа в общей форме: драйвер переводит в неё свой поток.
// Поток этапа в артефактах и в выводе `llamer run` — эти события в JSON.
type Event struct {
	Raw []byte `json:"-"` // строка вывода драйвера, из которой разобрано событие
	// Kind — тип события у драйвера: его называет сообщение о молчании этапа.
	Kind string `json:"kind"`
	// Failed — драйвер пометил событие ошибкой: лог показывает его строку целиком.
	Failed  bool     `json:"failed,omitempty"`
	Session *Session `json:"session,omitempty"` // начало сессии
	Note    string   `json:"note,omitempty"`    // служебное событие драйвера одной строкой
	Stderr  string   `json:"stderr,omitempty"`  // вывод мимо потока: stderr процесса или ошибка драйвера
	// Unparsed — строка вывода, которую драйвер не разобрал.
	Unparsed string `json:"unparsed,omitempty"`
	// Start — начало ответа модели: с него кэш промпта, записанный запросом,
	// доступен соседним запросам. Ход модели — тоже её ответ.
	Start     bool       `json:"start,omitempty"`
	Turn      *Turn      `json:"turn,omitempty"`      // ход модели
	Replies   []Reply    `json:"replies,omitempty"`   // ответы инструментов
	Result    *Result    `json:"result,omitempty"`    // итог этапа
	RateLimit *RateLimit `json:"rateLimit,omitempty"` // сведения о лимитах подписки
}

// Session — начало сессии этапа.
type Session struct {
	ID    string   `json:"id"`
	Model string   `json:"model,omitempty"` // пусто — драйвер модель не назвал
	Tools int      `json:"tools"`           // инструментов у сессии
	MCP   []string `json:"mcp,omitempty"`   // серверы MCP: «имя=статус»
}

// Turn — ход модели: текст, размышления и вызовы инструментов по порядку.
type Turn struct {
	MessageID string  `json:"messageId,omitempty"` // пусто — драйвер ходы не нумерует
	Usage     *Usage  `json:"usage,omitempty"`     // токены запроса хода; nil — не названы
	Blocks    []Block `json:"blocks"`
}

// Block — часть хода: текст, размышление или вызов инструмента.
type Block struct {
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	Call     *Call  `json:"call,omitempty"`
}

// Call — вызов инструмента.
type Call struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"` // аргументы, как их показывает лог
	// Command — команда оболочки; File — файл, который вызов читает; Skill — скилл,
	// который он грузит. Пусто — вызов не такой.
	Command string `json:"command,omitempty"`
	File    string `json:"file,omitempty"`
	Skill   string `json:"skill,omitempty"`
}

// Reply — ответ инструмента на вызов CallID.
type Reply struct {
	CallID  string `json:"callId"`
	Text    string `json:"text"`
	IsError bool   `json:"isError,omitempty"`
}

// Result — итог этапа.
type Result struct {
	Text      string           `json:"text"` // ответ или текст ошибки
	IsError   bool             `json:"isError,omitempty"`
	APIStatus *int             `json:"apiStatus,omitempty"` // код ответа API, на котором этап сорвался; nil — не назван
	Cost      *float64         `json:"cost"`                // nil — драйвер цены не назвал
	Usage     map[string]Usage `json:"usage,omitempty"`     // токены по моделям; nil — не названы
}

// RateLimit — сведения драйвера о лимитах подписки.
type RateLimit struct {
	Raw      json.RawMessage `json:"raw"`                // как их прислал драйвер: так они ложатся в журнал срыва
	Limits   *Limits         `json:"limits,omitempty"`   // nil — не разобраны
	ResetsAt *int64          `json:"resetsAt,omitempty"` // сброс лимита; nil — не назван
}
