package drivers

import (
	"fmt"
	"regexp"

	"github.com/hakastein/llamer/internal/agent"
)

// Tally — свод потока этапа: итог, сессия, ходы, токены по ходам и лимиты — то, что драйверы собирают одинаково.
type Tally struct {
	t         *agent.Telemetry
	events    []*agent.Event
	result    *agent.Result
	SessionID string
	turns     int
	// Токены по ходам — страховка на случай гибели процесса без итога: у убитого
	// этапа счётчики иначе теряются, и отчёт показывает ноль при сожжённых миллионах.
	usage         agent.Usage
	sawUsage      bool
	contextTokens int64
	rateLimit     *agent.RateLimit
}

// Read — строки процесса p в общих событиях: каждое — в телеметрию t и в свод.
// Строку, которую parse не разобрал, телеметрия показывает как есть.
func Read(p *agent.Process, t *agent.Telemetry, parse func(line []byte) ([]*agent.Event, error)) *Tally {
	y := &Tally{t: t}
	for line := range p.Lines() {
		events, err := parse(line)
		if err != nil {
			t.Unparsed(string(line))
			continue
		}
		for _, e := range events {
			y.add(e)
		}
	}
	return y
}

func (y *Tally) add(e *agent.Event) {
	y.t.Accept(e)
	y.events = append(y.events, e)
	if e.Result != nil {
		y.result = e.Result
	}
	if e.Session != nil && e.Session.ID != "" {
		y.SessionID = e.Session.ID
	}
	if e.RateLimit != nil {
		y.rateLimit = e.RateLimit
	}
	if e.Turn != nil {
		y.turns++
		if u := e.Turn.Usage; u != nil {
			y.sawUsage = true
			y.usage.InputTokens += u.InputTokens
			y.usage.OutputTokens += u.OutputTokens
			y.usage.CacheReadInputTokens += u.CacheReadInputTokens
			y.usage.CacheCreationInputTokens += u.CacheCreationInputTokens
			y.contextTokens = max(y.contextTokens, u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens)
		}
	}
}

// Outcome — исход этапа по выходу его процесса и своду потока. Цена этапа без
// итога — ноль; токены без итога — сумма ходов, оценка снизу.
func (y *Tally) Outcome(exit agent.Exit, model, effort string) *agent.Outcome {
	r := y.result
	if r == nil {
		code, signal := "нет", exit.Signal
		if exit.Code != nil {
			code = fmt.Sprint(*exit.Code)
		}
		if signal == "" {
			signal = "нет"
		}
		y.t.Unparsed("процесс завершился без итогового события: код " + code + ", сигнал " + signal)
	}
	o := &agent.Outcome{Model: model, Effort: effort, ExitCode: exit.Code, Signal: exit.Signal, Result: r, Events: y.events,
		OK:         exit.Code != nil && *exit.Code == 0 && r != nil && !r.IsError && !exit.Interrupted,
		DurationMs: exit.DurationMs, ContextTokens: y.contextTokens, Turns: y.turns}
	if !o.OK {
		o.Reason = failureReason(r, exit)
	}
	if r == nil {
		zero := 0.0
		o.Cost = &zero
	} else {
		o.Cost = r.Cost
	}
	if r != nil && r.Usage != nil {
		o.Usage = r.Usage
	} else if y.sawUsage {
		o.Usage = map[string]agent.Usage{"stream": y.usage}
		o.UsageApprox = true
	}
	if rl := y.rateLimit; rl != nil {
		o.RateLimit, o.ResetsAt, o.Limits = rl.Raw, rl.ResetsAt, rl.Limits
	}
	return o
}

var rateLimitText = regexp.MustCompile(`(?i)usage limit reached|rate limit|out of credits`)

// failureReason — причина срыва этапа. Код 0 не значит успех — при ошибке
// причина читается из итога, а текст ловит исчерпание подписки и без статуса.
func failureReason(r *agent.Result, exit agent.Exit) string {
	if r != nil {
		if status := r.APIStatus; status != nil {
			switch *status {
			case 401, 403:
				return "auth"
			case 429:
				return "rate_limit"
			}
		}
		if rateLimitText.MatchString(r.Text) {
			return "rate_limit"
		}
	}
	switch {
	case exit.TimedOut:
		return "timeout"
	case r == nil && exit.Signal != "":
		return "killed:" + exit.Signal
	case r == nil:
		return "died"
	}
	return "other"
}
