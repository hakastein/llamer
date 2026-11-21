package workflow

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/broker"
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

// runScript — workflow script на заглушке, которая запоминает запуски; сокеты команд не открываются.
func runScript(t *testing.T, script string) (*Result, *recorder) {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"workflows/w.js": `const llamer = require("llamer");
exports.definition = {short: "w", long: "W."};
const s = {driver: "claude", model: "m", effort: "low", timeout: "1m", prompt: "p"};
exports.command = async () => {` + script + `};
`})
	p := project(t, dir)
	run, err := artifacts.Open(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := &recorder{}
	result, err := (&Run{Context: t.Context(), Project: p, Driver: func(string) (drivers.Driver, error) { return d, nil },
		Artifacts: run, Cwd: dir, Out: io.Discard, Bin: t.TempDir(), ClientDir: t.TempDir(), Listen: unserved}).Execute(
		p.Workflows.Commands[0], cliche.Input{})
	if err != nil {
		t.Fatal(err)
	}
	return result, d
}

// allow этапа — префиксы любых команд, не только из commands/: они доходят до разрешений драйвера. Метасимвол оболочки
// разрешил бы цепочку чего угодно — ошибка спека.
func TestStageAllowAnyCommand(t *testing.T) {
	result, d := runScript(t, `return await llamer.agent({...s, name: "a", allow: ["rg", "git log"]}).result;`)
	if !result.OK || len(d.runs) != 1 || !slices.Equal(d.runs[0].Allow, []string{"rg", "git log"}) {
		t.Fatalf("результат: %+v, запуски: %+v", result, d.runs)
	}
	result, d = runScript(t, `return await llamer.agent({...s, name: "a", allow: ["rg; sh"]}).result;`)
	if result.OK || result.Failure.Reason != "spec" || len(d.runs) != 0 {
		t.Fatalf("результат: %+v", result)
	}
}

// Сокет команд этапа открыт на чтение только его песочнице: через сокет соседа агент звал бы команды от его имени.
func TestStageSocketOnlyInOwnSandbox(t *testing.T) {
	result, d := runScript(t, `return await Promise.all(["a", "b"].map((name) => llamer.agent({...s, name, allow: ["note"]}).result));`)
	if !result.OK || len(d.runs) != 2 {
		t.Fatalf("результат: %+v", result)
	}
	sockets := map[string]string{}
	for _, a := range d.runs {
		for _, e := range a.Env {
			if socket, ok := strings.CutPrefix(e, broker.SocketEnv+"="); ok {
				sockets[a.Name] = socket
			}
		}
	}
	for _, a := range d.runs {
		for name, socket := range sockets {
			open := slices.ContainsFunc(a.Sandbox.Paths(a, ""), func(p string) bool {
				return socket == p || strings.HasPrefix(socket, p+string(filepath.Separator))
			})
			if open != (name == a.Name) {
				t.Errorf("этапу %s сокет этапа %s открыт: %v", a.Name, name, open)
			}
		}
	}
}
