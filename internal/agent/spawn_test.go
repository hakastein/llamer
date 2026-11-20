package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hakastein/llamer/internal/agent"
)

type processStream struct {
	ready chan struct{}
	once  sync.Once
}

func (s *processStream) Write(raw []byte) {
	var e struct{ Type, Text string }
	if json.Unmarshal(raw, &e) == nil && e.Type == "stage_stderr" && e.Text == "ready" {
		s.once.Do(func() { close(s.ready) })
	}
}
func (*processStream) End() {}

// parseResult — итог из строки `{"result": …}`.
func parseResult(line []byte) ([]*agent.Event, error) {
	var e struct{ Result string }
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, err
	}
	return []*agent.Event{{Raw: line, Result: &agent.Result{Text: e.Result}}}, nil
}

func TestSpawnCancellationIsLocalAndKillsDescendants(t *testing.T) {
	for _, byTimeout := range []bool{false, true} {
		name := "context"
		if byTimeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			start := func(ctx context.Context, script string, timeout time.Duration) (<-chan struct{}, <-chan *agent.Record) {
				stream := &processStream{ready: make(chan struct{})}
				telemetry := agent.NewTelemetry(name, nil, nil, dir, stream, func() {}, agent.CommandProtocol{})
				result := make(chan *agent.Record, 1)
				go func() {
					launch := &agent.Launch{Command: "/bin/sh", Argv: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"},
						Timeout: timeout, Parse: parseResult}
					result <- telemetry.Finish(agent.Spawn(ctx, name, launch, dir, telemetry))
				}()
				return stream.ready, result
			}
			awaitReady := func(ready <-chan struct{}) {
				t.Helper()
				select {
				case <-ready:
				case <-time.After(3 * time.Second):
					t.Fatal("процесс не запустился")
				}
			}
			awaitResult := func(done <-chan *agent.Record) *agent.Record {
				t.Helper()
				select {
				case r := <-done:
					return r
				case <-time.After(3 * time.Second):
					t.Fatal("дочерний процесс удерживает поток после отмены")
					return nil
				}
			}
			otherCtx, stopOther := context.WithCancel(context.Background())
			defer stopOther()
			ready, other := start(otherCtx, `echo ready >&2; while [ ! -e release ]; do sleep 0.01; done; printf '%s\n' '{"result":"done"}'`, 0)
			awaitReady(ready)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := time.Duration(0)
			if byTimeout {
				timeout = 100 * time.Millisecond
			}
			// Потомок держит оба потока: убийство одной оболочки не даст Spawn вернуться.
			ready, done := start(ctx, `sleep 30 & echo ready >&2; wait`, timeout)
			awaitReady(ready)
			if !byTimeout {
				cancel()
			}
			r := awaitResult(done)
			if r.OK || r.Signal != "SIGKILL" || r.TimedOut != byTimeout {
				t.Fatalf("исход отменённого запуска: %+v", r)
			}
			select {
			case r := <-other:
				t.Fatalf("отмена задела независимый запуск: %+v", r)
			default:
			}
			if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			r = awaitResult(other)
			if !r.OK || r.Result.Text != "done" || len(r.Events) == 0 {
				t.Fatalf("независимый запуск не вернул результат и события: %+v", r)
			}
		})
	}
}
