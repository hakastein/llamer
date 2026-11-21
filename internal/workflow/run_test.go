package workflow

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/drivers"
)

// meeting — заглушка, этап которой отвечает, только когда запущены оба этапа.
type meeting struct {
	mu   sync.Mutex
	in   int
	both chan struct{}
}

func (d *meeting) Run(ctx context.Context, a drivers.Args, t *agent.Telemetry) (*agent.Outcome, error) {
	d.mu.Lock()
	if d.in++; d.in == 2 {
		close(d.both)
	}
	d.mu.Unlock()
	select {
	case <-d.both:
		return stub{}.Run(ctx, a, t)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Этапы под Promise.all идут одновременно: этап, запущенный после ответа другого, не дождался бы его.
func TestStagesUnderPromiseAllRunTogether(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"workflows/pair.js": `const llamer = require("llamer");
exports.definition = {short: "pair", long: "Two stages at once."};
exports.command = async () => {
  const s = {driver: "claude", model: "m", effort: "low", timeout: "1m", prompt: "p"};
  return await Promise.all([llamer.agent({...s, name: "a"}).result, llamer.agent({...s, name: "b"}).result]);
};
`})
	p := project(t, dir)
	run, err := artifacts.Open(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Срок — только чтобы последовательный запуск упал, а не повис.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	d := &meeting{both: make(chan struct{})}
	result, err := (&Run{Context: ctx, Project: p, Driver: func(string) (drivers.Driver, error) { return d, nil },
		Artifacts: run, Cwd: dir, Out: io.Discard}).Execute(p.Workflows.Commands[0], cliche.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Stages) != 2 {
		t.Fatalf("результат: %+v", result)
	}
}
