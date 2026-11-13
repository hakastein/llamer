package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Отбор изменений задачи. Ветки как в проекте с двумя общими ветками: release
// и develop ответвлены от main, ветка задачи ответвляется от одной и вливается
// в другую. Исключаются целевая ветка и общие — как их передаёт вызывающий:
// --exclude origin/<ветка>.
var shared = []string{"release", "main"}

func commit(t *testing.T, dir string, files map[string]string, message string) string {
	for path, content := range files {
		write(t, dir, path, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

func upstream(t *testing.T) string {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", "test")
	git(t, dir, "config", "user.email", "test@localhost")
	git(t, dir, "config", "uploadpack.allowFilter", "true")
	commit(t, dir, map[string]string{"shared.php": "один\nдва\nтри\n", "core.php": "ядро\n"}, "начало")
	git(t, dir, "branch", "release")
	git(t, dir, "branch", "develop")
	git(t, dir, "checkout", "-q", "release")
	commit(t, dir, map[string]string{"release-only.php": "релиз\n"}, "в релизе")
	git(t, dir, "checkout", "-q", "develop")
	commit(t, dir, map[string]string{"develop-only.php": "разработка\n"}, "в develop")
	git(t, dir, "checkout", "-q", "main")
	commit(t, dir, map[string]string{"main-only.php": "main\n"}, "в main")
	return dir
}

func branchFrom(t *testing.T, dir, name, from string) {
	git(t, dir, "checkout", "-q", "-b", name, from)
}

func merge(t *testing.T, dir, branch string) {
	git(t, dir, "merge", "-q", "--no-ff", "--no-edit", branch)
}

// mergeConflicted — слияние с конфликтом: его разрешает следующий commit.
func mergeConflicted(t *testing.T, dir, branch string) {
	cmd := exec.Command("git", "merge", "-q", "--no-ff", "--no-edit", branch)
	cmd.Dir = dir
	_ = cmd.Run()
}

// clone — клон, как в CI: ветки — в origin, дерево — на голове задачи; историю догрузил вызывающий.
func clone(t *testing.T, dir, head string, shallow bool) string {
	path := t.TempDir()
	args := []string{"clone", "-q", dir, path}
	if shallow {
		args = []string{"clone", "-q", "--depth", "1", "--no-single-branch", "file://" + dir, path}
	}
	git(t, ".", args...)
	git(t, path, "checkout", "-q", "--detach", head)
	return path
}

// scopeConfig — конфиг вне дерева: только версия и драйвер, этапы встроенные.
func scopeConfig(t *testing.T) string {
	file := filepath.Join(t.TempDir(), ".llamer.json")
	write(t, filepath.Dir(file), ".llamer.json", `{"version": 2, "driver": "claude"}`)
	return file
}

func excludes(target string) []string {
	var args []string
	for _, branch := range append([]string{target}, shared...) {
		args = append(args, "--exclude", "origin/"+branch)
	}
	return args
}

func scope(t *testing.T, dir, head, target string, shallow bool) *result {
	ws := clone(t, dir, head, shallow)
	return run(t, ws, append([]string{"--config", scopeConfig(t), "--head", head}, excludes(target)...), nil)
}

// block — блок «Что ревьюилось» из report.md: файлы разбора и отсеянные.
type block struct {
	text              string
	files, excluded   []string
	conflicted, bases []string
}

var (
	fileRow     = regexp.MustCompile("(?m)^- `([^`]+)` \\+\\d+ −\\d+( — по диффу реквеста)?$")
	excludedRow = regexp.MustCompile("(?m)^- `([^`]+)`$")
	baseItem    = regexp.MustCompile("`([^`@]+)@[0-9a-f]{8}`")
)

func scopeOf(t *testing.T, r *result) block {
	t.Helper()
	report := read(t, r.str("report"))
	start := strings.Index(report, "<details><summary>Что ревьюилось")
	if start < 0 {
		t.Fatalf("в report.md нет блока «Что ревьюилось»:\n%s", report)
	}
	b := block{text: report[start:]}
	main, excluded, _ := strings.Cut(b.text, "<details><summary>Отсеянные файлы</summary>")
	for _, m := range fileRow.FindAllStringSubmatch(main, -1) {
		b.files = append(b.files, m[1])
		if m[2] != "" {
			b.conflicted = append(b.conflicted, m[1])
		}
	}
	for _, m := range excludedRow.FindAllStringSubmatch(excluded, -1) {
		b.excluded = append(b.excluded, m[1])
	}
	if bases := regexp.MustCompile(`(?m)^Базы: (.*)$`).FindStringSubmatch(main); bases != nil {
		for _, m := range baseItem.FindAllStringSubmatch(bases[1], -1) {
			b.bases = append(b.bases, m[1])
		}
	}
	slices.Sort(b.files)
	slices.Sort(b.excluded)
	return b
}

// taskSide — строки одной стороны диффа задачи у файла: «+» — добавленные, «-» — удалённые.
func taskSide(t *testing.T, r *result, file, sign string) []string {
	diff := read(t, filepath.Join(r.str("artifacts"), "task.diff"))
	var lines []string
	for _, section := range strings.Split(diff, "diff --git ") {
		if !strings.HasPrefix(section, "a/"+file+" ") {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, sign) && !strings.HasPrefix(line, sign+sign+sign) {
				lines = append(lines, line[1:])
			}
		}
	}
	return lines
}

func TestScopeReleaseIntoRelease(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	head := commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")

	r := scope(t, dir, head, "release", false)

	ok(t, r)
	b := scopeOf(t, r)
	sameList(t, b.files, []string{"task.php"}, "файлы разбора")
	sameList(t, b.excluded, nil, "отсеянные")
	match(t, b.text, "Изменения задачи: всё, чего ещё нет в `origin/release`, `origin/main`\\.", "база — исключённые ревизии без повторов")
	noMatch(t, b.text, `конфликту`, "конфликтов нет")
	sameList(t, b.bases, []string{"origin/release", "origin/main"}, "базы")
}

func TestScopeReleaseIntoDevelop(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	head := commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")

	b := scopeOf(t, scope(t, dir, head, "develop", false))

	sameList(t, b.files, []string{"task.php"}, "файлы разбора")
	sameList(t, b.excluded, []string{"release-only.php"}, "изменения release отсеяны")
	match(t, b.text, "Отсеяно как чужие изменения: 1 файл диффа реквеста\\.", "строка отсеянного")
}

func TestScopeMergedReleaseKeepsConflictResolution(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	commit(t, dir, map[string]string{"task.php": "задача\n", "shared.php": "один\nдва задачи\nтри\n"}, "задача")
	git(t, dir, "checkout", "-q", "release")
	commit(t, dir, map[string]string{"release-two.php": "релиз 2\n", "shared.php": "один\nдва релиза\nтри\n"}, "ещё в релизе")
	git(t, dir, "checkout", "-q", "PROJ-10")
	mergeConflicted(t, dir, "release")
	head := commit(t, dir, map[string]string{"shared.php": "один\nдва задачи и релиза\nтри\n"}, "Merge release into PROJ-10")

	r := scope(t, dir, head, "develop", false)

	b := scopeOf(t, r)
	sameList(t, b.files, []string{"shared.php", "task.php"}, "файлы разбора")
	sameList(t, b.excluded, []string{"release-only.php", "release-two.php"}, "влитый release отсеян")
	sameList(t, taskSide(t, r, "shared.php", "+"), []string{"два задачи и релиза"}, "разрешение конфликта — своё")
}

func TestScopeCrossMergedSubtaskExcluded(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-1", "release")
	branchFrom(t, dir, "PROJ-2", "release")
	commit(t, dir, map[string]string{"sub.php": "подзадача\n"}, "подзадача")
	git(t, dir, "checkout", "-q", "PROJ-1")
	merge(t, dir, "PROJ-2")
	branchFrom(t, dir, "PROJ-3", "PROJ-1")
	commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")
	git(t, dir, "checkout", "-q", "release")
	commit(t, dir, map[string]string{"release-two.php": "релиз 2\n"}, "ещё в релизе")
	git(t, dir, "checkout", "-q", "PROJ-3")
	merge(t, dir, "release")
	head := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "PROJ-1")
	merge(t, dir, "release")

	b := scopeOf(t, scope(t, dir, head, "PROJ-1", false))

	sameList(t, b.files, []string{"task.php"}, "подзадача, уже влитая в целевую, отсеяна")
}

func TestScopeDevelopMergedIntoReleaseBranchStays(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")
	merge(t, dir, "develop")
	head := git(t, dir, "rev-parse", "HEAD")

	b := scopeOf(t, scope(t, dir, head, "release", false))

	sameList(t, b.files, []string{"develop-only.php", "task.php"}, "develop в release нет — он в разборе")
}

func TestScopeConflictBetweenSharedBranches(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	git(t, dir, "checkout", "-q", "main")
	commit(t, dir, map[string]string{"shared.php": "один\nдва из main\nтри\n"}, "в main")
	git(t, dir, "checkout", "-q", "release")
	commit(t, dir, map[string]string{"shared.php": "один\nдва релиза\nтри\n"}, "в релизе")
	branchFrom(t, dir, "PROJ-10", "release")
	commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")
	mergeConflicted(t, dir, "main")
	head := commit(t, dir, map[string]string{"shared.php": "один\nдва из main и релиза\nтри\n"}, "Merge main into PROJ-10")

	r := scope(t, dir, head, "release", false)

	b := scopeOf(t, r)
	match(t, b.text, `Между исключёнными ревизиями конфликтует 1 файл: по ним разобран дифф реквеста\.`, "конфликт назван")
	sameList(t, b.conflicted, []string{"shared.php"}, "конфликтный файл — по диффу реквеста")
	sameList(t, b.files, []string{"shared.php", "task.php"}, "файлы разбора")
	sameList(t, b.excluded, []string{"main-only.php"}, "отсеянные")
	sameList(t, taskSide(t, r, "shared.php", "-"), []string{"два релиза"}, "старая сторона — от базы реквеста")
}

func TestScopeNothingOwn(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	head := git(t, dir, "rev-parse", "release")

	r := scope(t, dir, head, "develop", false)

	ok(t, r)
	equal(t, len(r.launches), 0, "этапы не поднимаются")
	match(t, read(t, r.str("report")), `Разбирать нечего: голова не добавляет изменений к базе разбора\. Ревью пропущено\.`, "report.md")
	b := scopeOf(t, r)
	sameList(t, b.files, nil, "файлы разбора")
	sameList(t, b.excluded, []string{"release-only.php"}, "отсеянные")
}

func TestScopeShallowCloneWithoutHistory(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	head := commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")

	r := viaMR(t, dir, head, true, nil)

	equal(t, r.status, 1, "код")
	failure := r.answer["failure"].(obj)
	equal(t, failure["reason"], any("scope"), "причина")
	match(t, failure["detail"].(string), `нет общей истории — клон неглубокий`, "подробность называет причину")
}

// sinceLast — прогон --since-last по MR в develop: метки прошлых прогонов — в заметке-статусе.
func sinceLast(t *testing.T, dir, head string, marks ...string) *result {
	return viaMR(t, dir, head, false, []string{"--since-last"}, marks...)
}

// viaMR — прогон по MR в develop, как в CI: базу и голову даёт GitLab.
func viaMR(t *testing.T, dir, head string, shallow bool, extra []string, marks ...string) *result {
	ws := clone(t, dir, head, shallow)
	base := git(t, dir, "merge-base", "develop", head)
	var notes []any
	for _, mark := range marks {
		notes = append(notes, obj{"id": "status-" + mark, "individual_note": true, "notes": []obj{{
			"body": "<!-- llamer-status -->\nСтатус.\n\n<sub>ревизия</sub><!-- llamer-rev:" + mark + " -->", "author": obj{"username": "bot"}}}})
	}
	g := startGitlab(t, project, iid,
		obj{"title": "Задача", "source_branch": "PROJ-10", "target_branch": "develop", "diff_refs": obj{"base_sha": base, "head_sha": head, "start_sha": base}},
		changesOf(t, dir, base, head), []obj{{"head_commit_sha": head, "base_commit_sha": base, "start_commit_sha": base}}, notes)
	args := append([]string{"--config", scopeConfig(t), "--mr", g.URL + "/" + project + "/-/merge_requests/" + iid}, extra...)
	for _, branch := range shared {
		args = append(args, "--exclude", "origin/"+branch)
	}
	return run(t, ws, args, nil)
}

func TestSinceLastMergedSharedAfterMarkExcluded(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	mark := commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")
	git(t, dir, "checkout", "-q", "release")
	commit(t, dir, map[string]string{"release-late.php": "поздний релиз\n"}, "релиз ушёл вперёд")
	git(t, dir, "checkout", "-q", "PROJ-10")
	merge(t, dir, "release")
	head := commit(t, dir, map[string]string{"task-2.php": "правка после прогона\n"}, "после прогона")

	r := sinceLast(t, dir, head, mark)

	ok(t, r)
	match(t, r.stderr, `только изменения после прошлого прогона `+mark[:8], "от метки")
	b := scopeOf(t, r)
	sameList(t, b.files, []string{"task-2.php"}, "влитое после метки отсеяно")
	sameList(t, b.bases, []string{"origin/develop", "origin/release", "origin/main", "прошлый прогон"}, "базы")
	match(t, b.text, `Отсеяно как чужие или уже разобранные прошлым прогоном изменения`, "отсеянное")
}

func TestSinceLastLaterOfTwoMarks(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	early := commit(t, dir, map[string]string{"task.php": "задача\n"}, "первый прогон")
	late := commit(t, dir, map[string]string{"task-2.php": "правка\n"}, "второй прогон")
	head := commit(t, dir, map[string]string{"task-3.php": "после прогонов\n"}, "после прогонов")

	for _, marks := range [][]string{{late, early}, {early, late}} {
		r := sinceLast(t, dir, head, marks...)

		ok(t, r)
		equal(t, r.str("base"), late, "счёт от поздней метки")
		sameList(t, scopeOf(t, r).files, []string{"task-3.php"}, "файлы разбора")
	}
}

func TestSinceLastMarkAtRequestBase(t *testing.T) {
	t.Parallel()
	dir := upstream(t)
	branchFrom(t, dir, "PROJ-10", "release")
	head := commit(t, dir, map[string]string{"task.php": "задача\n"}, "задача")
	base := git(t, dir, "merge-base", "develop", head)

	r := sinceLast(t, dir, head, base)

	ok(t, r)
	b := scopeOf(t, r)
	sameList(t, b.files, []string{"task.php"}, "весь реквест")
	sameList(t, b.bases, []string{"origin/develop", "origin/release", "origin/main"}, "без прошлого прогона")
	if data, _ := json.Marshal(r.answer); strings.Contains(string(data), "прошлый прогон") {
		t.Error("метка на базе реквеста — не прошлый прогон")
	}
}
