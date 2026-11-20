package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/logx"
)

const silenceReport = 30 * time.Second

// Начало ответа модели: с этого момента кэш промпта, записанный запросом,
// доступен соседним запросам.
func isResponseStart(e *Event) bool { return e.Start || e.Turn != nil }

// Stream — сырой поток этапа в артефактах.
type Stream interface {
	Write(line []byte)
	End()
}

// Telemetry — лог и поток этапа по его событиям, статистика и итог в лог.
type Telemetry struct {
	mu         sync.Mutex
	name       string
	cwd        string
	skills     []string
	commands   []string
	stream     Stream
	events     []*Event
	lastShown  string
	heartbeat  *time.Ticker
	stop       chan struct{}
	onResponse func()
	traffic    *Traffic
}

// NewTelemetry — телеметрия этапа name. commands и skills — команды и скиллы метода этапа: статистика называет
// их и тогда, когда агент к ним не обращался; onResponse — начало ответа модели.
func NewTelemetry(name string, commands, skills []string, cwd string, stream Stream, onResponse func()) *Telemetry {
	t := &Telemetry{name: name, cwd: cwd, skills: skills, commands: commands, stream: stream, onResponse: onResponse,
		traffic: newTraffic(), stop: make(chan struct{})}
	startedAt := time.Now()
	t.heartbeat = time.NewTicker(silenceReport)
	go func(ticker *time.Ticker, stop chan struct{}) {
		for {
			select {
			case <-ticker.C:
				t.Logf("%sс без единого события — %s", seconds(time.Since(startedAt).Milliseconds()), t.traffic.Describe())
			case <-stop:
				return
			}
		}
	}(t.heartbeat, t.stop)
	return t
}

// Logf — строка лога об этапе.
func (t *Telemetry) Logf(format string, args ...any) {
	logx.Logf("этап «%s»: %s", t.name, fmt.Sprintf(format, args...))
}

func (t *Telemetry) stopHeartbeat() {
	if t.heartbeat != nil {
		t.heartbeat.Stop()
		close(t.stop)
		t.heartbeat = nil
	}
}

// Accept — событие драйвера.
func (t *Telemetry) Accept(e *Event) {
	t.traffic.mu.Lock()
	t.traffic.events++
	t.traffic.lastEventType = e.Kind
	t.traffic.mu.Unlock()
	t.record(e)
}

// Unparsed — строка вывода, которую драйвер не разобрал, или иное сообщение о выводе этапа.
func (t *Telemetry) Unparsed(text string) { t.own("stage_raw", text) }

// own — событие llamer о процессе этапа: вывод stderr или неразобранная строка.
func (t *Telemetry) own(typ, text string) {
	raw, _ := json.Marshal(map[string]string{"type": typ, "name": t.name, "text": text})
	e := &Event{Raw: raw, Kind: typ, Stderr: text}
	if typ == "stage_raw" {
		e = &Event{Raw: raw, Kind: typ, Unparsed: text}
	}
	t.record(e)
}

func (t *Telemetry) record(event *Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if isResponseStart(event) {
		t.onResponse()
	}
	t.stopHeartbeat()
	t.stream.Write(event.Raw)
	t.events = append(t.events, event)
	// Цвет в логе всегда, и без терминала: так у JS-версии (isTTY вне терминала — undefined, и срабатывал colors = true по умолчанию).
	shown, ok := FormatStreamEvent(event, true, t.cwd, t.name)
	if !ok || shown == "" || shown == t.lastShown {
		return
	}
	t.lastShown = shown
	logx.Stamped(shown)
}

// Close — этап закончен: поток закрыт, сообщения о молчании остановлены.
func (t *Telemetry) Close() {
	t.mu.Lock()
	t.stopHeartbeat()
	t.mu.Unlock()
	t.stream.End()
}

// Finish — этап закончен исходом o: статистика и итог в лог.
func (t *Telemetry) Finish(o *Outcome) {
	t.Close()
	for _, line := range StatsFrom(t.events, t.cwd, t.skills, t.commands).Lines() {
		logx.Logf("[%s] %s", t.name, line)
	}
	outcome := "завершён"
	if !o.OK {
		detail := ""
		if o.Result == nil {
			code := "?"
			if o.ExitCode != nil {
				code = fmt.Sprint(*o.ExitCode)
			}
			if o.Signal != "" {
				code += ", " + o.Signal
			}
			detail = " [exit " + code + "]"
		}
		outcome = "не выполнен (" + o.Reason + ")" + detail
	}
	logx.Logf("этап «%s» %s: %sс, %s", t.name, outcome, seconds(o.DurationMs), FormatCost(o))
	if !o.OK {
		t.Logf("%s", t.traffic.Describe())
	}
}

// FormatCost — цена этапа. Токены, восстановленные из потока убитого этапа, —
// оценка снизу; цену, которую драйвер не назвал, не выдумываем.
func FormatCost(o *Outcome) string {
	if o.UsageApprox || o.Cost == nil {
		return "$?"
	}
	return fmt.Sprintf("$%.2f", *o.Cost)
}

func seconds(ms int64) string { return fmt.Sprint((ms + 500) / 1000) }
