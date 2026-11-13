package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/logx"
)

const silenceReport = 30 * time.Second

// Начало ответа модели: с этого момента кэш промпта, записанный запросом,
// доступен соседним запросам.
func isResponseStart(e *Event) bool {
	return (e.Type == "stream_event" && e.Inner.Type == "message_start") || e.Type == "assistant"
}

// Stream — сырой поток этапа в артефактах.
type Stream interface {
	Write(line []byte)
	End()
}

// Telemetry — лог и статистика этапа по его событиям.
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
	Traffic    *Traffic
}

func NewTelemetry(name string, commands, skills []string, cwd string, stream Stream, onResponse func()) *Telemetry {
	t := &Telemetry{name: name, cwd: cwd, skills: skills, commands: commands, stream: stream, onResponse: onResponse,
		Traffic: newTraffic(), stop: make(chan struct{})}
	startedAt := time.Now()
	t.heartbeat = time.NewTicker(silenceReport)
	go func(ticker *time.Ticker, stop chan struct{}) {
		for {
			select {
			case <-ticker.C:
				logx.Logf("этап «%s»: %sс без единого события — %s", name, seconds(time.Since(startedAt).Milliseconds()), t.Traffic.Describe())
			case <-stop:
				return
			}
		}
	}(t.heartbeat, t.stop)
	return t
}

func (t *Telemetry) stopHeartbeat() {
	if t.heartbeat != nil {
		t.heartbeat.Stop()
		close(t.stop)
		t.heartbeat = nil
	}
}

func (t *Telemetry) Accept(event *Event) {
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

// Finish — итог этапа: статистика в лог, причина срыва, лимиты.
func (t *Telemetry) Finish(r *Record) *Record {
	t.mu.Lock()
	t.stopHeartbeat()
	t.mu.Unlock()
	t.stream.End()
	stats := StatsFrom(t.events, t.cwd, t.skills, t.commands, harness.WriteCommands)
	for _, line := range stats.Lines() {
		logx.Logf("[%s] %s", t.name, line)
	}
	if !r.OK {
		r.Reason = FailureReason(r)
	}
	outcome := "завершён"
	if !r.OK {
		detail := ""
		if r.Result == nil {
			code := "?"
			if r.ExitCode != nil {
				code = fmt.Sprint(*r.ExitCode)
			}
			if r.Signal != "" {
				code += ", " + r.Signal
			}
			detail = " [exit " + code + "]"
		}
		outcome = "не выполнен (" + r.Reason + ")" + detail
	}
	logx.Logf("этап «%s» %s: %sс, %s", t.name, outcome, seconds(r.DurationMs), FormatCost(r))
	if !r.OK {
		logx.Logf("этап «%s»: %s", t.name, t.Traffic.Describe())
	}
	r.RateLimit = CollectRateLimit(t.events)
	if r.Limits == nil {
		r.Limits = LimitsOf(r.RateLimit)
	}
	r.LostWrites = stats.LostWrites
	return r
}
