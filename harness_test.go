package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Команда `review` этапа — скрытая подкоманда llamer: её зовёт агент, задание
// этапа — в LLAMER_BRIEF.

const taskDiff = "diff --git a/src/A.php b/src/A.php\n--- a/src/A.php\n+++ b/src/A.php\n@@ -2,1 +2,2 @@\n-old\n+new\n+newer\n"

type called struct {
	status         int
	stdout, stderr string
}

func callHarness(t *testing.T, dir, brief string, args ...string) called {
	t.Helper()
	cmd := exec.Command(llamer, append([]string{"__harness"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LLAMER_BRIEF="+brief)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	c := called{stdout: stdout.String(), stderr: stderr.String()}
	if exit, ok := err.(*exec.ExitError); ok {
		c.status = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return c
}

type stageOpts struct {
	name, journalDir string
	threads          []obj
	relatedRequired  *bool
	siblings         []string
}

type harnessStage struct {
	t               *testing.T
	dir, journalDir string
	brief           string
}

// stage — этап с заданием на дифф taskDiff. Несколько этапов одного прогона —
// общий journalDir, у каждого свой файл журнала.
func stage(t *testing.T, commands []string, o stageOpts) *harnessStage {
	dir := t.TempDir()
	if o.name == "" {
		o.name = "logic"
	}
	if o.journalDir == "" {
		o.journalDir = filepath.Join(dir, "journal")
	}
	if err := os.MkdirAll(o.journalDir, 0o777); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/A.php", "one\nnew\nnewer\n")
	write(t, dir, "task.diff", taskDiff)
	b := obj{"stage": o.name, "name": o.name, "journalDir": o.journalDir, "baseSha": strings.Repeat("a", 40),
		"diffPath": filepath.Join(dir, "task.diff"), "commands": commands, "threads": o.threads, "siblings": o.siblings}
	if b["threads"] == nil {
		b["threads"] = []obj{}
	}
	if b["siblings"] == nil {
		b["siblings"] = []string{}
	}
	if o.relatedRequired != nil {
		b["relatedRequired"] = *o.relatedRequired
	}
	data, _ := json.Marshal(b)
	write(t, dir, "brief.json", string(data))
	return &harnessStage{t, dir, o.journalDir, filepath.Join(dir, "brief.json")}
}

func (s *harnessStage) call(args ...string) called {
	s.t.Helper()
	return callHarness(s.t, s.dir, s.brief, args...)
}

// events — события журнала прогона в порядке записи (`at`).
func (s *harnessStage) events(kind string) []obj {
	var out []obj
	entries, _ := os.ReadDir(s.journalDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(s.journalDir, e.Name()))
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var event obj
			if json.Unmarshal([]byte(line), &event) == nil && event["type"] == kind {
				out = append(out, event)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b obj) int { return int(a["at"].(float64) - b["at"].(float64)) })
	return out
}

func finding(line string) []string {
	return []string{"finding", "--file", "src/A.php", "--line", line, "--line-end", line,
		"--text", "текст", "--severity", "major", "--severity-reason", "вес", "--related", "src/A.php:" + line}
}

func TestHarnessFindingOnChangedLine(t *testing.T) {
	t.Parallel()
	s := stage(t, []string{"finding", "list", "stop"}, stageOpts{})

	equal(t, s.call(finding("3")...).status, 0, "находка на изменённой строке")
	refused := s.call(finding("1")...)

	equal(t, refused.status, 1, "отказ")
	match(t, refused.stderr, `отказ: строка 1 в src/A\.php не изменена.*Изменённые строки: 2, 3`, "текст отказа")
	findings := s.events("finding")
	equal(t, len(findings), 1, "записано")
	equal(t, fmt.Sprint(findings[0]["line"], findings[0]["stage"]), "3logic", "находка")
}

func TestHarnessAmendSuggestionChecked(t *testing.T) {
	t.Parallel()
	s := stage(t, []string{"finding", "amend", "stop"}, stageOpts{})
	equal(t, s.call(finding("3")...).status, 0, "находка")

	same := s.call("amend", "--id", "1", "--suggestion", "newer")
	equal(t, same.status, 1, "правка совпадает с кодом")
	match(t, same.stderr, `правка совпадает с текущим кодом`, "текст отказа")
	equal(t, s.call("amend", "--id", "1", "--suggestion", "newest").status, 0, "правка принята")
	amends := s.events("amend")
	equal(t, len(amends), 1, "amend записан")
	equal(t, amends[0]["suggestion"], any("newest"), "правка")
}

func TestHarnessForeignCommandRefused(t *testing.T) {
	t.Parallel()
	s := stage(t, []string{"finding", "batch", "stop"}, stageOpts{})

	single := s.call("verdict", "--id", "1", "--refuted", "true", "--reason", "x")
	equal(t, single.status, 1, "отказ")
	match(t, single.stderr, `команды «verdict» у этапа «logic» нет`, "текст")

	events, _ := json.Marshal([]obj{
		{"type": "verdict", "id": 1, "refuted": true, "reason": "x"},
		{"type": "finding", "file": "src/A.php", "line": 2, "lineEnd": 2, "text": "t", "severity": "minor", "severityReason": "r", "related": "src/A.php:2"},
	})
	batch := s.call("batch", "--json", string(events))
	equal(t, batch.status, 0, batch.stderr)
	match(t, batch.stdout, `элемент 1: типа «verdict» среди команд этапа нет`, "предупреждение")
	equal(t, len(s.events("finding")), 1, "находка из батча записана")
	equal(t, len(s.events("verdict")), 0, "чужая команда не записана")
}

func TestHarnessUsageAndHelp(t *testing.T) {
	t.Parallel()
	s := stage(t, []string{"finding", "stop"}, stageOpts{})

	match(t, s.call("stop", "--why", "x").stderr, `отказ: неизвестный флаг`, "неизвестный флаг")
	match(t, s.call("stop").stderr, `отказ: нужен --reason: why the review was aborted`, "пропущенный обязательный")
	help := s.call("finding", "--help")
	equal(t, help.status, 0, "справка")
	// Колонка флагов — по самому длинному, описание переносится в 80 символов.
	match(t, help.stdout, `^Usage: review finding \[options\]\n\nrecord a remark on the request, anchored to diff lines\n`, "справка")
	match(t, help.stdout, `\n {2}--line-end <number> {7}last line of it; no default, so repeat --line for a\n {28}one-line remark \[required\]\n`, "перенос")
	match(t, help.stdout, `\(choices: "minor", "major", "critical"\)`, "варианты")
	match(t, help.stdout, `\n {2}-h, --help {16}display this message\n`, "help")
	match(t, s.call("--help").stdout, `Stage commands:\n {2}finding`, "общая справка")
}

func TestHarnessRelatedOptionalForComments(t *testing.T) {
	t.Parallel()
	bare := finding("3")[:len(finding("3"))-2]
	no := false

	match(t, stage(t, []string{"finding"}, stageOpts{}).call(bare...).stderr, `нужен --related`, "разбирающий этап")
	equal(t, stage(t, []string{"finding"}, stageOpts{relatedRequired: &no}).call(bare...).status, 0, "comments")
}

func TestHarnessBatchCoercesJSON(t *testing.T) {
	t.Parallel()
	s := stage(t, []string{"batch", "finding"}, stageOpts{})
	events, _ := json.Marshal([]obj{{"type": "finding", "file": "src/A.php", "line": 2, "lineEnd": 3, "deleted": false,
		"text": "t", "severity": "minor", "severityReason": "r", "related": "src/A.php"}})

	done := s.call("batch", "--json", string(events))

	equal(t, done.status, 0, done.stderr)
	equal(t, s.events("finding")[0]["deleted"], any(false), "false у --deleted не делает находку удалённой")
}

func TestHarnessDupeAfterClockStepBack(t *testing.T) {
	t.Parallel()
	// Находки записаны, пока часы спешили на минуту: их `at` — в будущем.
	s := stage(t, []string{"dupe", "list"}, stageOpts{name: "dedupe"})
	found, _ := json.Marshal(obj{"type": "finding", "file": "src/A.php", "line": 2, "lineEnd": 2, "text": "t", "severity": "major",
		"at": time.Now().Add(time.Minute).UnixMilli(), "stage": "logic"})
	write(t, s.journalDir, "logic.jsonl", string(found)+"\n"+string(found)+"\n")

	done := s.call("dupe", "--id", "2", "--of", "1", "--reason", "то же место")

	equal(t, done.status, 0, done.stderr)
	match(t, s.call("list").stdout, `(?m)^2\. \[повтор #1\] src/A\.php:2`, "dupe лёг после своих находок")
}

func TestHarnessResolvedThreadKeepsFoldOrder(t *testing.T) {
	t.Parallel()
	// Файлы журнала читаются по имени: service.dedupe, service.threads, tests.
	tests := stage(t, []string{"finding", "list"}, stageOpts{name: "tests"})
	equal(t, tests.call(finding("2")...).status, 0, "находка")
	equal(t, tests.call(finding("3")...).status, 0, "находка")
	threads := stage(t, []string{"resolve"}, stageOpts{name: "service.threads", journalDir: tests.journalDir,
		threads: []obj{{"id": "t1", "at": "src/Order.php:4", "text": "замечание"}}})
	equal(t, threads.call("resolve", "--id", "t1", "--reason", "снято").status, 0, "resolve")
	dedupe := stage(t, []string{"dupe"}, stageOpts{name: "service.dedupe", journalDir: tests.journalDir})
	equal(t, dedupe.call("dupe", "--id", "2", "--of", "1", "--reason", "то же").status, 0, "dupe")

	match(t, tests.call("list").stdout, `(?m)^2\. \[повтор #1\] src/A\.php:3`, "dupe не потерялся")
	resolved := tests.events("resolve")
	equal(t, fmt.Sprint(resolved[0]["id"], " ", resolved[0]["reason"], " ", resolved[0]["place"]), "t1 снято src/Order.php:4", "закрытый тред")
}

func TestHarnessSiblingInstancesHidden(t *testing.T) {
	t.Parallel()
	claude := stage(t, []string{"finding", "list"}, stageOpts{name: "logic·claude", siblings: []string{"logic·codex"}})
	equal(t, claude.call(finding("3")...).status, 0, "находка claude")
	codex := stage(t, []string{"finding", "amend", "list"}, stageOpts{name: "logic·codex", journalDir: claude.journalDir, siblings: []string{"logic·claude"}})

	equal(t, strings.TrimSpace(codex.call("list").stdout), "находок пока нет", "находок соседа не видно")
	match(t, codex.call("amend", "--id", "1", "--text", "чужая").stderr, `находки #1 в журнале нет`, "чужую не поправить")
	equal(t, codex.call(finding("3")...).status, 0, "своя находка")
	match(t, codex.call("list").stdout, `^2\. \[major\] src/A\.php:3`, "номер сквозной по прогону")
	equal(t, codex.call("amend", "--id", "2", "--text", "своя").status, 0, "свою поправить")

	quality := stage(t, []string{"list"}, stageOpts{name: "quality", journalDir: claude.journalDir})
	equal(t, len(strings.Split(strings.TrimSpace(quality.call("list").stdout), "\n")), 2, "другой этап видит все")
}

// diffRepo — база и голова с файлами всех статусов; изменение в середине
// длинного файла — ширина контекста видна. Задание `review diff` — на эти ревизии.
type diffRepo struct {
	root, brief, base, head string
}

func numbers(from, to int) string {
	var b strings.Builder
	for n := from; n <= to; n++ {
		fmt.Fprintf(&b, "%d\n", n)
	}
	return b.String()
}

func makeDiffRepo(t *testing.T) diffRepo {
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "config", "user.email", "t@example.com")
	git(t, root, "config", "user.name", "t")
	write(t, root, ".llamer.json", `{"version": 2, "driver": "claude"}`)
	write(t, root, "src/Kept.php", numbers(1, 30))
	write(t, root, "src/Gone.php", numbers(1, 30))
	write(t, root, "src/Old.php", numbers(1, 20))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	write(t, root, "src/Kept.php", strings.Replace(numbers(1, 30), "\n15\n", "\nfifteen\n", 1))
	git(t, root, "rm", "-q", "src/Gone.php")
	git(t, root, "mv", "src/Old.php", "src/New.php")
	write(t, root, "src/New.php", numbers(1, 21))
	write(t, root, "src/Fresh.php", "a\nb\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "head")
	head := git(t, root, "rev-parse", "HEAD")
	data, _ := json.Marshal(obj{"stage": "logic", "journalDir": filepath.Join(root, ".git"), "baseSha": base, "revs": []string{base, head},
		"diffPath": filepath.Join(root, ".git", "HEAD"), "commands": []string{"diff"}})
	write(t, root, ".git/brief.json", string(data))
	return diffRepo{root, filepath.Join(root, ".git", "brief.json"), base, head}
}

func (d diffRepo) call(t *testing.T, args ...string) called {
	t.Helper()
	return callHarness(t, d.root, d.brief, args...)
}

// pack — дифф пакета прогона на тех же ревизиях: общая часть задания этапа.
func (d diffRepo) pack(t *testing.T) string {
	r := run(t, d.root, []string{"--base", d.base, "--head", d.head}, nil)
	ok(t, r)
	shared := r.launch(t, "assess")["shared"].(string)
	return shared[strings.Index(shared, "<diff>\n")+len("<diff>\n") : strings.Index(shared, "\n</diff>")]
}

func section(pack, file string) string {
	for _, chunk := range regexp.MustCompile(`(?m)^diff --git `).Split(pack, -1) {
		if strings.HasSuffix(strings.SplitN(chunk, "\n", 2)[0], " b/"+file) {
			return strings.TrimRight("diff --git "+chunk, "\n")
		}
	}
	return ""
}

func TestReviewDiffMatchesPack(t *testing.T) {
	t.Parallel()
	d := makeDiffRepo(t)
	pack := d.pack(t)
	match(t, section(pack, "src/Kept.php"), `\n {4}5  5\n[\s\S]*\n {5} -15\n {3}15 \+fifteen\n[\s\S]*\n {3}25  25$`, "контекст пакета — десять строк")
	match(t, section(pack, "src/Gone.php"), `\n {6}-1\n {6}-2\n[\s\S]*\n {6}-30$`, "удалённый файл в пакете целиком")

	one := d.call(t, "diff", "src/Kept.php")
	equal(t, one.status, 0, one.stderr)
	equal(t, one.stdout, section(pack, "src/Kept.php")+"\n", "файл — как в секции пакета")

	several := d.call(t, "diff", "src/Gone.php", "src/Missing.php", "src/New.php")
	equal(t, several.status, 0, several.stderr)
	equal(t, several.stdout, strings.Join([]string{section(pack, "src/Gone.php"),
		"src/Missing.php: среди изменений задачи такого файла нет; перечень — review diff без аргументов",
		section(pack, "src/New.php"), ""}, "\n"), "несколько путей, путь вне изменений — строка")
	match(t, several.stdout, `\n {6}-1\n {6}-2\n[\s\S]*\n {6}-30\n`, "review diff удалённого файла — всё удалённое")
}

func TestReviewDiffRename(t *testing.T) {
	t.Parallel()
	d := makeDiffRepo(t)
	renamed := section(d.pack(t), "src/New.php")
	match(t, renamed, `^diff --git a/src/Old\.php b/src/New\.php\nsimilarity index \d+%\nrename from src/Old\.php\nrename to src/New\.php\n`, "rename")
	var changed []string
	for _, row := range strings.Split(renamed, "\n") {
		if regexp.MustCompile(`^ *\d* [+-]`).MatchString(row) {
			changed = append(changed, row)
		}
	}
	sameList(t, changed, []string{"   21 +21"}, "только изменённая строка")
	equal(t, d.call(t, "diff", "src/New.php").stdout, renamed+"\n", "review diff")
}

func TestReviewDiffList(t *testing.T) {
	t.Parallel()
	d := makeDiffRepo(t)

	equal(t, d.call(t, "diff").stdout, strings.Join([]string{
		"src/Fresh.php — добавлен, +2 −0",
		"src/Gone.php — удалён, +0 −30",
		"src/Kept.php — изменён, +1 −1",
		"src/New.php — переименован из src/Old.php, +1 −0",
		"",
	}, "\n"), "перечень")
	match(t, d.call(t, "diff", "--help").stdout, `^Usage: review diff \[options\] \[path\.\.\.\]\n\nthe task's diff of the given files`, "справка")
	match(t, d.call(t, "--help").stdout, `\n {2}diff {2}the task's diff of the given files`, "общая справка")
}

// Тред находки на переименованном файле встаёт по новому пути со старым.
func TestRenamedFileThreadPosition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "config", "user.email", "t@example.com")
	git(t, root, "config", "user.name", "t")
	write(t, root, ".llamer.json", `{"version": 2, "driver": "claude"}`)
	write(t, root, "src/Old.php", numbers(1, 20))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	git(t, root, "mv", "src/Old.php", "src/New.php")
	write(t, root, "src/New.php", numbers(1, 21))
	git(t, root, "commit", "-qam", "head")
	d := diffRepo{root: root, base: git(t, root, "rev-parse", "HEAD~1"), head: git(t, root, "rev-parse", "HEAD")}
	git(t, d.root, "update-ref", "refs/remotes/origin/main", d.base)
	files := []obj{{"new_path": "src/New.php", "old_path": "src/Old.php", "new_file": false, "deleted_file": false, "renamed_file": true,
		"diff": "@@ -18,3 +18,4 @@\n 18\n 19\n 20\n+21\n"}}
	g := startGitlab(t, project, iid,
		obj{"title": "R", "source_branch": "r", "target_branch": "main", "diff_refs": obj{"base_sha": d.base, "head_sha": d.head, "start_sha": d.base}},
		files, []obj{{"head_commit_sha": d.head, "base_commit_sha": d.base, "start_commit_sha": d.base}}, nil)
	r := run(t, d.root, []string{"--mr", g.URL + "/" + project + "/-/merge_requests/" + iid, "--publish", "gitlab"}, nil)

	ok(t, r)
	var position obj
	for _, w := range g.writes() {
		if p, ok := w.Body["position"].(obj); ok && p["new_path"] == "src/New.php" {
			position = p
		}
	}
	if position == nil {
		t.Fatalf("треда на src/New.php нет: %v", g.writes())
	}
	equal(t, position["old_path"], any("src/Old.php"), "старый путь")
	equal(t, position["new_line"], any(float64(21)), "строка")
}

func hook(t *testing.T, command string) called {
	t.Helper()
	input, _ := json.Marshal(obj{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": obj{"command": command}})
	cmd := exec.Command(llamer, "__claude-hook")
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	return called{stdout: stdout.String(), stderr: stderr.String()}
}

func allowed(t *testing.T, command string) bool {
	t.Helper()
	answer := hook(t, command).stdout
	if answer == "" {
		return false
	}
	var decision struct {
		HookSpecificOutput struct{ PermissionDecision string }
	}
	if err := json.Unmarshal([]byte(answer), &decision); err != nil {
		t.Fatal(err)
	}
	return decision.HookSpecificOutput.PermissionDecision == "allow"
}

// Хук — ровно тот, что драйвер кладёт в --settings этапа: его команду проверяют прогоны claude.
func TestHookAllowsLongMultilineBatch(t *testing.T) {
	t.Parallel()
	// Форма батча из срыва: многострочный JSON с кириллицей и кодом, длиннее
	// 10 000 символов — предела, за которым Claude Code команду не разбирает.
	var items []string
	for n := range 120 {
		items = append(items, fmt.Sprintf(`{"type":"finding","file":"src/A.php","line":%d,"text":"Ключ '"'"'identity'"'"' => $analysis['\''map'\''] и \"кавычки\" в `+"`коде`"+`"}`, n))
	}
	batch := "review batch --json '[\n" + strings.Join(items, ",\n") + "\n]'"
	equal(t, len(batch) > 10_000, true, "батч длиннее предела")
	equal(t, allowed(t, batch), true, "батч разрешён")
}

func TestHookSilentOnAnythingButSimpleCall(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"review batch --json 'x'; rm -rf ~",
		"review batch --json 'x'\nrm -rf ~",
		"review batch --json 'x' && id",
		"review batch --json 'x' ; id",
		`review batch --json 'x'$'\x41'`,
		"review batch --json 'x' | sh",
		"review batch --json 'x' > /tmp/f",
		`review batch --json "$(id)"`,
		"review batch --json 'x'$(id)",
		"review batch --json 'x'`id`",
		"review batch --json $HOME",
		"review batch --json 'незакрытая",
		"cat x | review batch --json 'x'",
		"reviewer batch --json 'x'",
	} {
		equal(t, hook(t, command).stdout, "", command)
	}
}

func TestHookAllowsDiffLoop(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`for f in src/A.php src/B.php; do review diff "$f"; done`,
		"for file in src/A.php\ndo\n  review diff $file\ndone\n",
		`for f in a b; do review diff "$f" src/C.php "$f"; done | head -50`,
		`for f in @x/y+z_1.mjs -a; do review diff "$f"; done | tail -20`,
	} {
		equal(t, allowed(t, command), true, command)
	}
}

func TestHookSilentOnOtherLoops(t *testing.T) {
	t.Parallel()
	commands := []string{
		`for f in a; do review diff "$f"; rm x; done`,
		`for f in $(ls); do review diff "$f"; done`,
		`for f in a; do cat "$f"; done`,
		`for f in a; do review diff "$g"; done`,
		`for f in a; do review diff "$f$(id)"; done`,
		`for f in a; do review diff "$f" > /tmp/x; done`,
		`for f in a; do review diff "$f"; done | sh`,
		`for f in a; do review diff "$f"; done | head -5; id`,
		`for f in a; do review diff "$f"; done; id`,
		`for f in a; do review diff "$f"; done && id`,
		`id; for f in a; do review diff "$f"; done`,
		`for f in a; do review batch "$f"; done`,
	}
	for _, word := range []string{"$HOME", `"a"`, "'a'", "`id`", "*.php", "a?", "(a)", "a;id", "a|id", "a&id", "a<b", "a>b"} {
		commands = append(commands, `for f in a `+word+`; do review diff "$f"; done`)
	}
	for _, command := range commands {
		equal(t, hook(t, command).stdout, "", command)
	}
}
