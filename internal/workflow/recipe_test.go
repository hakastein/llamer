package workflow

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	_ "github.com/hakastein/llamer/internal/drivers/claude" // драйвер этапов рецепта
)

// recipe — рецепт ревью из скилла llamer.
var recipe = filepath.Join("..", "..", "skills", "llamer", "references", "review")

func project(t *testing.T, dir string) *Project {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// changedTree — репозиторий с коммитом и изменениями поверх него: правкой строки и неотслеживаемым файлом.
func changedTree(t *testing.T) string {
	t.Helper()
	tree := t.TempDir()
	gitIn(t, tree, "init", "-q")
	writeFiles(t, tree, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	gitIn(t, tree, "add", ".")
	gitIn(t, tree, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	writeFiles(t, tree, map[string]string{"a.txt": "one\nTWO\nthree\n", "new.txt": "fresh\n"})
	return tree
}

// recorder — заглушка --dry-run, которая запоминает запуски.
type recorder struct {
	mu   sync.Mutex
	runs []drivers.Args
}

func (d *recorder) Run(ctx context.Context, a drivers.Args, t *agent.Telemetry) (*agent.Outcome, error) {
	d.mu.Lock()
	d.runs = append(d.runs, a)
	d.mu.Unlock()
	return stub{}.Run(ctx, a, t)
}

// Workflow рецепта проходит на заглушке агента; allow каждого этапа — команды из раздела «Your Commands» его задания.
func TestReviewRecipeStages(t *testing.T) {
	p := project(t, recipe)
	i := slices.IndexFunc(p.Workflows.Commands, func(c *cliche.Command) bool { return slices.Equal(c.Path, []string{"review"}) })
	run, err := artifacts.Open(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := &recorder{}
	result, err := (&Run{Context: context.Background(), Project: p, Driver: func(string) (drivers.Driver, error) { return d, nil },
		Artifacts: run, Cwd: changedTree(t), Out: io.Discard, Listen: unserved}).Execute(p.Workflows.Commands[i],
		cliche.Input{Flags: cliche.NewObject([]string{"base"}, []any{"HEAD"})})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(d.runs) != 7 {
		t.Fatalf("результат: %+v", result)
	}
	listed := regexp.MustCompile("(?m)^Your commands are (.*)$")
	quoted := regexp.MustCompile("`([^`]+)`")
	for _, a := range d.runs {
		var commands []string
		for _, m := range quoted.FindAllStringSubmatch(listed.FindStringSubmatch(a.Prompt)[1], -1) {
			commands = append(commands, m[1])
		}
		if !slices.Equal(commands, a.Allow) {
			t.Errorf("%s: allow %q, в задании %q", a.Name, a.Allow, commands)
		}
		if !strings.Contains(a.Prompt, "+TWO") {
			t.Errorf("%s: в задании нет изменений", a.Name)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Находка встаёт только на строки изменений от базы из записей workflow: добавленные — по новой стороне, удалённые —
// с --deleted по старой; неотслеживаемый файл — тоже изменение.
func TestReviewRecipeAnchorsFindings(t *testing.T) {
	tree := changedTree(t)
	head, err := exec.Command("git", "-C", tree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	c := &commands{catalog: project(t, recipe).Commands, journal: newJournal(t.TempDir())}
	log := []any{map[string]any{"stage": "materials", "value": map[string]any{"type": "materials", "base": strings.TrimSpace(string(head))}}}
	s := &stage{Name: "logic", Allow: []string{"review"}, Programs: []string{"git", "cat"}, Data: log}
	call := func(argv ...string) (string, int) {
		r, _ := c.call(context.Background(), s, broker.Call{Argv: argv, Cwd: tree})
		return r.Stdout + r.Stderr, r.Exit
	}
	finding := func(file, line string, extra ...string) int {
		_, code := call(append([]string{"review", "finding", "--file", file, "--line", line, "--line-end", line, "--text", "t",
			"--severity", "minor", "--severity-reason", "r", "--related", file}, extra...)...)
		return code
	}
	for _, c := range []struct {
		name       string
		code, want int
	}{
		{"добавленная строка", finding("a.txt", "2"), 0},
		{"удалённая строка", finding("a.txt", "2", "--deleted"), 0},
		{"неотслеживаемый файл", finding("new.txt", "1"), 0},
		{"строка без изменений", finding("a.txt", "1"), 1},
		{"удалённой строки нет", finding("a.txt", "1", "--deleted"), 1},
	} {
		if c.code != c.want {
			t.Errorf("%s: код %d", c.name, c.code)
		}
	}
	if got := len(c.journal.recorded("logic")); got != 3 {
		t.Fatalf("записано находок: %d", got)
	}
}

// probeScript — команда над библиотеками рецепта: текст с экранированными `$` и код строки GitLab для пути.
const probeMarkupScript = `const {escapeDollarsOutsideCode} = require("../lib/markdown.js");
const {Diff} = require("../lib/diff.js");
const gitlab = require("../lib/gitlab.js");
exports.definition = {short: "probe", long: "Probe.", args: [{name: "text", type: "string", usage: "t"}, {name: "path", type: "string", usage: "p"}]};
exports.command = (text, path) => {
  const diff = new Diff("diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n");
  const p = gitlab.position(path, 1, 1, false, diff, {});
  return escapeDollarsOutsideCode(text) + "\n" + p.line_range.start.line_code;
};
`

// Разметка публикации — как у GitLab-публикации встроенного ревью (ожидания записаны с неё): `$` вне кода
// экранируется, в спанах и блоках — нет; код строки — SHA-1 пути в UTF-8.
func TestReviewRecipeMarkup(t *testing.T) {
	source := t.TempDir()
	lib := filepath.Join(recipe, "lib")
	entries, err := os.ReadDir(lib)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"commands/probe.js": probeMarkupScript}
	for _, e := range entries {
		files["lib/"+e.Name()] = readFile(t, filepath.Join(lib, e.Name()))
	}
	writeFiles(t, source, files)
	c := &commands{catalog: project(t, source).Commands, journal: newJournal(t.TempDir())}
	const path = "src/Заказ.php"
	sum := sha1.Sum([]byte(path))
	for _, tc := range [][2]string{
		{"a $x `$y` b", "a \\$x `$y` b"},
		{"```\n$a\n```\n$b", "```\n$a\n```\n\\$b"},
		{"text\n\n    $code\n$c", "text\n\n    $code\n\\$c"},
		{"`` a`$b `` $c", "`` a`$b `` \\$c"},
		{"unclosed `$x", "unclosed `\\$x"},
		{"para\n    $not code", "para\n    \\$not code"},
		{"~~~php\n$a\n~~~~\n$b", "~~~php\n$a\n~~~~\n\\$b"},
	} {
		r, _ := c.call(context.Background(), &stage{Name: "publish", Allow: []string{"probe"}},
			broker.Call{Argv: []string{"probe", tc[0], path}, Cwd: source})
		want := tc[1] + "\n" + hex.EncodeToString(sum[:]) + "_2_1\n"
		if r.Exit != 0 || r.Stdout != want {
			t.Errorf("%q: %d %q %s, ожидалось %q", tc[0], r.Exit, r.Stdout, r.Stderr, want)
		}
	}
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
