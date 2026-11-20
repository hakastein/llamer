package workflow

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
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

// probeScript — команда над библиотеками рецепта: текст с экранированными `$` и код строки GitLab для пути.
const probeMarkupScript = `const {escapeDollarsOutsideCode} = require("./lib/markdown.js");
const {Diff} = require("./lib/diff.js");
const gitlab = require("./lib/gitlab.js");
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
	lib := filepath.Join(recipe, config.CommandsDir, "lib")
	entries, err := os.ReadDir(lib)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"probe.js": probeMarkupScript}
	for _, e := range entries {
		files["lib/"+e.Name()] = readFile(t, filepath.Join(lib, e.Name()))
	}
	writeFiles(t, source, files)
	catalog := cliche.Merge(cliche.Config{Sources: []cliche.Source{cliche.Dir(source)}}.Scan())
	c := &commands{workflow: &config.Workflow{Commands: catalog}, journal: newJournal(t.TempDir())}
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
		r := c.handler("publish", make(chan error, 1))(context.Background(), broker.Call{Argv: []string{"probe", tc[0], path}, Cwd: source})
		want := tc[1] + "\n" + hex.EncodeToString(sum[:]) + "_2_1\n"
		if r.Exit != 0 || r.Stdout != want {
			t.Errorf("%q: %d %q %s, ожидалось %q", tc[0], r.Exit, r.Stdout, r.Stderr, want)
		}
	}
}
