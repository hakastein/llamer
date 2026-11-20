package workflow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/config"
)

// recipe — рецепт ревью из скилла llamer.
var recipe = filepath.Join("..", "..", "skills", "llamer", "references", "review")

// Рецепт загружается как есть: шаблоны ссылаются только на прошлые волны, скиллы и команды на месте.
func TestReviewRecipeLoads(t *testing.T) {
	dir, err := filepath.Abs(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadWorkflow(dir, map[string]string{"REVIEW_BASE": "HEAD", "REVIEW_TASK": ""}); err != nil {
		t.Fatal(err)
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

// Находка встаёт только на строки изменений: добавленные — по новой стороне, удалённые — с --deleted по старой;
// неотслеживаемый файл — тоже изменение.
func TestReviewRecipeAnchorsFindings(t *testing.T) {
	commandsDir, err := filepath.Abs(filepath.Join(recipe, config.CommandsDir))
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	gitIn(t, tree, "init", "-q")
	writeFiles(t, tree, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	gitIn(t, tree, "add", ".")
	gitIn(t, tree, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	writeFiles(t, tree, map[string]string{"a.txt": "one\nTWO\nthree\n", "new.txt": "fresh\n"})
	catalog := cliche.Merge(cliche.Config{Sources: []cliche.Source{cliche.Dir(commandsDir)}}.Scan())
	c := &commands{workflow: &config.Workflow{Commands: catalog, Programs: []string{"git", "cat"}}, journal: newJournal(t.TempDir())}
	call := func(argv ...string) (string, int) {
		r := c.handler("logic", make(chan error, 1))(context.Background(), broker.Call{Argv: argv, Cwd: tree})
		return r.Stdout + r.Stderr, r.Exit
	}
	if out, code := call("review", "materials", "--base", "HEAD", "--task", ""); code != 0 || !strings.Contains(out, "+TWO") {
		t.Fatalf("materials: %d %s", code, out)
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
	if out, _ := call("review", "list"); strings.Count(out, "\n") != 3 {
		t.Fatalf("list: %s", out)
	}
}
