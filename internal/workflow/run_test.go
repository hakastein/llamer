package workflow

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
)

// fakeDriver отвечает заданием этапа после паузы и начала ответа модели; шаги этапов — в steps.
type fakeDriver struct {
	mu    sync.Mutex
	steps []string
}

func (d *fakeDriver) step(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steps = append(d.steps, s)
}

func (d *fakeDriver) Run(_ context.Context, a drivers.Args, t *agent.Telemetry) (*agent.Outcome, error) {
	d.step("start " + a.Name)
	time.Sleep(20 * time.Millisecond)
	d.step("respond " + a.Name)
	t.Accept(&agent.Event{Kind: "start", Start: true})
	return &agent.Outcome{OK: true, Result: &agent.Result{Text: strings.TrimSpace(a.Prompt)}}, nil
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
			t.Fatal(err)
		}
	}
}

func stage(name string) string {
	return `{"name": "` + name + `", "driver": "claude", "model": "m", "effort": "low", "prompt": "` + name + `.md", "timeout": "1m"}`
}

// Волна: соседи ведущего стартуют, когда он начал отвечать; ответы волны и этапа-команды доходят до следующей.
func TestWaveAnswersReachNextWave(t *testing.T) {
	// Не t.TempDir(): имя теста в пути делает путь сокета длиннее лимита unix-сокета macOS (104 байта).
	dir, err := os.MkdirTemp("", "wf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	writeFiles(t, dir, map[string]string{
		"workflow.json": `{"version": 1, "stages": [[` + stage("lead") + `, ` + stage("side") + `],
			{"name": "note", "command": ["note", "--text", "${NOTE_TEXT}"]}, ` + stage("after") + `]}`,
		"lead.md":          "ведущий",
		"side.md":          "сосед",
		"after.md":         "{{.Answers.lead}}+{{.Answers.side}}+{{.Answers.note}}",
		"commands/note.js": noteScript,
	})
	w, err := config.LoadWorkflow(dir, map[string]string{"NOTE_TEXT": "из окружения"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := artifacts.Open(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{}
	result, err := (&Run{Context: context.Background(), Workflow: w, Drivers: map[string]drivers.Driver{"claude": d},
		Artifacts: run, Cwd: dir, Out: io.Discard, Bin: dir, ClientDir: dir}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Stages) != 4 {
		t.Fatalf("результат: %+v", result)
	}
	if got := string(result.Stages[3].Answer); got != `"ведущий+сосед+записано"` {
		t.Fatalf("ответ after: %s", got)
	}
	if d.steps[0] != "start lead" || slices.Index(d.steps, "start side") < slices.Index(d.steps, "respond lead") {
		t.Fatalf("шаги: %v", d.steps)
	}
	if text := readFile(t, filepath.Join(run.Path, JournalFile)); !strings.Contains(text, `"text":"из окружения"`) {
		t.Fatalf("журнал: %s", text)
	}
}

func TestWaveStageCannotReadNeighbour(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"workflow.json": `{"version": 1, "stages": [[` + stage("lead") + `, ` + stage("side") + `]]}`,
		"lead.md":       "ведущий",
		"side.md":       "{{.Answers.lead}}",
	})
	_, err := config.LoadWorkflow(dir, nil)
	if cfgErr := (*config.Error)(nil); !errors.As(err, &cfgErr) {
		t.Fatalf("ошибка: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
