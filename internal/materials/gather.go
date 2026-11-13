package materials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/logx"
	gl "gitlab.com/gitlab-org/api/client-go"
)

// DiffWidths — ширины контекста диффа пакета, от широкой к узкой.
var DiffWidths = []int{10, 6, 3, 0}

// Остаток пакета под задачу и треды.
const packReserve = 60_000

var diffBudget = PackLimit - packReserve

// Fitted — дифф пакета: текст, ширина контекста, свёрнутые удалённые файлы.
type Fitted struct {
	Text          string
	Context       int
	Folded, Saved int
}

// WidestFittingDiff — фиксированная ширина тратит бюджет на окружение, а режется
// дифф с конца. Полный дифф, когда влезает, идёт как есть; иначе первыми место
// уступают удалённые файлы, а ширина подбирается уже по свёрнутому диффу.
func WidestFittingDiff(diff *Diff, budget int) (Fitted, error) {
	text, err := diff.Text(DiffWidths[0])
	if err != nil {
		return Fitted{}, err
	}
	full := NumberDiffLines(text)
	if chars(full) <= budget {
		return Fitted{Text: full, Context: DiffWidths[0]}, nil
	}
	for i, context := range DiffWidths {
		text, err := diff.Text(context)
		if err != nil {
			return Fitted{}, err
		}
		numbered := NumberDiffLines(text)
		folded, n := FoldDeleted(numbered)
		if chars(folded) <= budget || i == len(DiffWidths)-1 {
			return Fitted{Text: folded, Context: context, Folded: n, Saved: chars(numbered) - chars(folded)}, nil
		}
	}
	panic("недостижимо")
}

var (
	diffStart   = regexp.MustCompile(`(?m)^diff --git `)
	hunkStart   = regexp.MustCompile(`(?m)^@@ `)
	devNullNew  = regexp.MustCompile(`(?m)^\+\+\+ /dev/null$`)
	deletedPath = regexp.MustCompile(`(?m)^--- "?a/(.*?)"?$`)
	deletedSize = regexp.MustCompile(`(?m)^@@ -1(?:,(\d+))? `)
)

// FoldDeleted — удалённый файл: его заголовки и одна строка-заглушка вместо содержимого.
func FoldDeleted(text string) (string, int) {
	var chunks []string
	last := 0
	for _, m := range diffStart.FindAllStringIndex(text, -1) {
		if m[0] > 0 {
			chunks = append(chunks, text[last:m[0]])
			last = m[0]
		}
	}
	chunks = append(chunks, text[last:])
	folded := 0
	for i, chunk := range chunks {
		// Строки ханка в пакете начинаются с отступа номера: с начала строки идут только заголовки.
		hunk := hunkStart.FindStringIndex(chunk)
		if hunk == nil {
			continue
		}
		head := chunk[:hunk[0]]
		if !devNullNew.MatchString(head) {
			continue
		}
		path := ""
		if m := deletedPath.FindStringSubmatch(head); m != nil {
			path = m[1]
		}
		lines := "1"
		if m := deletedSize.FindStringSubmatch(chunk[hunk[0]:]); m != nil && m[1] != "" {
			lines = m[1]
		}
		stub := head + "@@ -1," + lines + " +0,0 @@ deleted file, " + lines + " lines removed; the content: review diff " + path + "\n"
		// Короткий файл заглушка только удлинила бы.
		if chars(stub) >= chars(chunk) {
			continue
		}
		folded++
		chunks[i] = stub
	}
	return strings.Join(chunks, ""), folded
}

// Request — вход прогона: merge request GitLab или ревизии репозитория.
type Request interface {
	Kind() string
	HeadSha() string
	BaseSha() string
	Excludes() []string
	ExplicitBase() string
	RequestDiff(cwd string) (*Diff, error)
	Discussions() ([]*gl.Discussion, error)
}

func collectThreads(request Request) []Thread {
	if request.Kind() != "gitlab" {
		return []Thread{}
	}
	discussions, err := request.Discussions()
	if err != nil {
		logx.Logf("треды MR прочитать не удалось (%s) — разбор может повторить сказанное", err)
		return []Thread{}
	}
	threads := SummariseThreads(discussions)
	if len(threads) > 0 {
		logx.Logf("уже сказанное в MR: %d замечаний по строкам", len(threads))
	}
	return threads
}

// previousRunMarks — метки ревизии со сводок прошлых прогонов. Только из
// заметок, начинающихся маркером статуса: цитата сводки в ответе человека несёт ту же метку.
func previousRunMarks(request Request) []string {
	discussions, err := request.Discussions()
	if err != nil {
		logx.Logf("заметки MR для --since-last прочитать не удалось (%s) — разбирается весь реквест", err)
		return []string{}
	}
	var bodies []string
	for _, d := range discussions {
		for _, n := range d.Notes {
			if !n.System && strings.HasPrefix(n.Body, gitlab.StatusMarker) {
				bodies = append(bodies, n.Body)
			}
		}
	}
	marks := gitlab.CollectRevisionMarks(bodies)
	if marks == nil {
		marks = []string{}
	}
	return marks
}

// Scope — дешёвая часть: дифф реквеста и изменения задачи. Решение «есть ли
// что разбирать» принимается по ней — до сбора пакета.
type Scope struct {
	RequestDiff *Diff
	Task        *TaskChanges
	Diff        *Diff
	NothingOwn  bool
}

// PlanReviewScope — разбирается не дифф реквеста, а изменения задачи; дифф
// реквеста остаётся для публикации: треды GitLab встают только на его строки.
// Ошибка *TaskChangesError — изменения задачи не вычислены.
func PlanReviewScope(request Request, tree string, sinceLast bool) (*Scope, error) {
	requestDiff, err := request.RequestDiff(tree)
	if err != nil {
		return nil, err
	}
	var marks []string
	if sinceLast {
		marks = previousRunMarks(request)
	}
	task, err := ComputeTaskChanges(TaskArgs{
		Cwd: tree, Head: request.HeadSha(), Excludes: request.Excludes(), RequestDiff: requestDiff,
		RequestBase: request.BaseSha(), ExplicitBase: request.ExplicitBase(), SinceLast: sinceLast, SinceMarks: marks,
	})
	if err != nil {
		return nil, err
	}
	return &Scope{RequestDiff: requestDiff, Task: task, Diff: task.Diff, NothingOwn: task.Diff.IsEmpty()}, nil
}

// Gather — пакет контекста и материалы этапов. saveDiff кладёт дифф в артефакты прогона.
func Gather(request Request, tree string, scope *Scope, issue string, saveDiff func(name, text string) (string, error)) (*Materials, error) {
	if issue != "" {
		logx.Logf("текст задачи: %d символов", chars(issue))
	}
	diff := scope.Diff
	signals, err := CollectRiskSignals(diff)
	if err != nil {
		return nil, err
	}
	logx.Logf("размер изменений: %d файлов, %d изменённых строк", signals.Files, signals.ChangedLines)
	fitted, err := WidestFittingDiff(diff, diffBudget)
	if err != nil {
		return nil, err
	}
	if fitted.Folded > 0 {
		logx.Logf("удалённые файлы свёрнуты в заглушки: %d, сэкономлено %d символов", fitted.Folded, fitted.Saved)
	}
	logx.Logf("дифф в пакет: %d символов, контекст %d строк", chars(fitted.Text), fitted.Context)
	threads := collectThreads(request)
	if src := scope.RequestDiff.SourceText; src != nil && *src != "" {
		if _, err := saveDiff("request", *src); err != nil {
			return nil, err
		}
	}
	// Контекст как у GitLab (три строки): номер новой строки у удалённой строки
	// в таком диффе совпадает с диффом реквеста, по нему удалённая строка
	// переносится на строку реквеста при публикации.
	taskText, err := scope.Task.Diff.Text(3)
	if err != nil {
		return nil, err
	}
	diffPath, err := saveDiff("task", taskText)
	if err != nil {
		return nil, err
	}
	anchors, err := FromText(taskText, scope.Task.BaseSha, nil)
	if err != nil {
		return nil, err
	}
	m := &Materials{
		Diff: diff, Signals: signals, Issue: issue, Threads: threads, RequestDiff: scope.RequestDiff, Task: scope.Task,
		TaskAnchors: anchors, DiffPath: diffPath, Cwd: tree,
		pack: NewPack(fitted.Text, fitted.Folded, issue, threads),
	}
	logx.Logf("пакет контекста: общая часть %d символов, часть этапа %d", chars(m.SharedPack()), chars(m.RenderPack(true)))
	return m, nil
}

// TaskChangesError — изменения задачи вычислить не удалось: ревью без них не запускается.
type TaskChangesError struct{ Message string }

func (e *TaskChangesError) Error() string { return e.Message }

// Base — исключённая ревизия и её общий предок с головой.
type Base struct{ Rev, Sha string }

// TaskChanges — изменения задачи.
type TaskChanges struct {
	Kind       string // task, mixed, explicit
	Bases      []Base
	BaseSha    string
	Diff       *Diff
	Conflicted []string
	Excluded   []string
	Since      string // метка прошлого прогона или пусто
}

// MachineAuthor — автор служебных коммитов прогона.
var MachineAuthor = []string{
	"GIT_AUTHOR_NAME=llamer", "GIT_AUTHOR_EMAIL=llamer@localhost",
	"GIT_COMMITTER_NAME=llamer", "GIT_COMMITTER_EMAIL=llamer@localhost",
}

// На перекрёстных слияниях общих предков несколько, и любой один теряет то,
// что есть только в других: берутся все.
func mergeBasesWith(head, rev, cwd string) ([]string, error) {
	shas, ok := git.Try([]string{"merge-base", "--all", head, rev}, cwd)
	if !ok || shas == "" {
		return nil, &TaskChangesError{git.NoCommonHistory(head, rev, cwd)}
	}
	return strings.Split(shas, "\n"), nil
}

// mergeTree сливает две ревизии без рабочего дерева; merge-base git выбирает
// сам: явно заданный ломается на перекрёстных слияниях.
func mergeTree(ours, theirs, cwd string) (string, []string, error) {
	tree, err := git.Git([]string{"merge-tree", "--write-tree", "--name-only", "--no-messages", ours, theirs}, cwd)
	if err == nil {
		return tree, nil, nil
	}
	var gitErr *git.Error
	// Код 1 — слияние с конфликтами: дерево записано, ниже него конфликтные пути.
	if !errors.As(err, &gitErr) || gitErr.Status != 1 {
		return "", nil, &TaskChangesError{fmt.Sprintf("слияние %s и %s не собралось (%s)", git.Short(ours), git.Short(theirs), git.FirstLine(err))}
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(gitErr.Stdout), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "", nil, nil
	}
	return lines[0], lines[1:], nil
}

// takeFromRequestBase — конфликтный путь в слитом дереве содержит маркеры
// конфликта: такой файл берётся из базы реквеста — по нему ревьюится дифф
// реквеста: лишний чужой код в нём дешевле, чем пропущенный свой.
func takeFromRequestBase(tree string, paths []string, requestBase, cwd string) (string, error) {
	dir, err := os.MkdirTemp("", "llamer-base-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	env := "GIT_INDEX_FILE=" + filepath.Join(dir, "index")
	if _, err := git.Git([]string{"read-tree", tree}, cwd, env); err != nil {
		return "", err
	}
	for _, p := range paths {
		entry, err := git.Git([]string{"ls-tree", requestBase, "--", p}, cwd)
		if err != nil {
			return "", err
		}
		if entry != "" {
			meta, _, _ := strings.Cut(entry, "\t")
			fields := strings.Split(meta, " ")
			_, err = git.Git([]string{"update-index", "--add", "--cacheinfo", fields[0] + "," + fields[2] + "," + p}, cwd, env)
		} else {
			_, err = git.Git([]string{"update-index", "--force-remove", "--", p}, cwd, env)
		}
		if err != nil {
			return "", err
		}
	}
	return git.Git([]string{"write-tree"}, cwd, env)
}

// virtualBase — всё, что уже есть в исключённых ревизиях, в одном коммите.
// Коммит, а не голое дерево: этапы читают базу через `git show <sha>:<путь>`.
func virtualBase(bases []Base, requestBase, cwd string) (string, []string, error) {
	head := bases[0].Sha
	var conflicted []string
	for _, b := range bases[1:] {
		if git.IsAncestor(b.Sha, head, cwd) {
			continue
		}
		if git.IsAncestor(head, b.Sha, cwd) {
			head = b.Sha
			continue
		}
		tree, paths, err := mergeTree(head, b.Sha, cwd)
		if err != nil {
			return "", nil, err
		}
		for _, p := range paths {
			if !slices.Contains(conflicted, p) {
				conflicted = append(conflicted, p)
			}
		}
		if len(paths) > 0 {
			if tree, err = takeFromRequestBase(tree, conflicted, requestBase, cwd); err != nil {
				return "", nil, err
			}
		}
		if head, err = git.Git([]string{"commit-tree", "-p", head, "-p", b.Sha, "-m", "llamer: база изменений задачи", tree}, cwd, MachineAuthor...); err != nil {
			return "", nil, err
		}
	}
	sorted := slices.Clone(conflicted)
	slices.Sort(sorted)
	if sorted == nil {
		sorted = []string{}
	}
	return head, sorted, nil
}

// lastReviewed — метка прошлого прогона, от которой считать: последняя из тех,
// чья ревизия есть в истории головы. Метка вне истории — голова переписана.
// Совпавшая с базой реквеста ничего не сужает.
func lastReviewed(marks []string, head, requestBase, cwd string) string {
	var inHistory, dropped []string
	for _, sha := range marks {
		if git.IsAncestor(sha, head, cwd) {
			inHistory = append(inHistory, sha)
		}
	}
	for _, sha := range marks {
		if !slices.Contains(inHistory, sha) {
			dropped = append(dropped, git.Short(sha))
		}
	}
	if len(dropped) > 0 {
		logx.Log("--since-last: метки вне истории головы пропущены (force-push или rebase): " + strings.Join(dropped, ", "))
	}
	if len(inHistory) == 0 {
		logx.Log("--since-last: годной метки прошлого прогона нет — разбирается весь реквест")
		return ""
	}
	sha := inHistory[0]
	for _, b := range inHistory[1:] {
		if git.IsAncestor(sha, b, cwd) {
			sha = b
		}
	}
	if sha == requestBase {
		logx.Logf("--since-last: метка %s совпадает с базой реквеста — разбирается весь реквест", git.Short(sha))
		return ""
	}
	logx.Logf("--since-last: только изменения после прошлого прогона %s", git.Short(sha))
	return sha
}

// TaskArgs — вход вычисления изменений задачи.
type TaskArgs struct {
	Cwd, Head    string
	Excludes     []string
	RequestDiff  *Diff
	RequestBase  string
	ExplicitBase string // --base заменяет вычисление
	SinceLast    bool
	SinceMarks   []string
}

// ComputeTaskChanges — изменения задачи: то, чего ещё нет ни в одной из
// исключённых ревизий (целевая ветка MR и --exclude). Работа автора — коммиты,
// разрешения конфликтов и правки поверх влитых веток — это то, что голова
// добавляет к слиянию этих ревизий. --since-last добавляет ещё одну базу —
// метку прошлого прогона. Любая ошибка — *TaskChangesError.
func ComputeTaskChanges(a TaskArgs) (*TaskChanges, error) {
	t, err := computeTaskChanges(a)
	var tce *TaskChangesError
	if err != nil && !errors.As(err, &tce) {
		return nil, &TaskChangesError{"git не посчитал изменения задачи (" + git.FirstLine(err) + ")"}
	}
	return t, err
}

func computeTaskChanges(a TaskArgs) (*TaskChanges, error) {
	var bases []Base
	baseSha, conflicted, since := "", []string{}, ""
	if a.ExplicitBase != "" {
		bases = []Base{{"--base", a.ExplicitBase}}
		baseSha = a.ExplicitBase
	} else {
		var seen []string
		for _, rev := range a.Excludes {
			if slices.Contains(seen, rev) {
				continue
			}
			seen = append(seen, rev)
			shas, err := mergeBasesWith(a.Head, rev, a.Cwd)
			if err != nil {
				return nil, err
			}
			for _, sha := range shas {
				bases = append(bases, Base{rev, sha})
			}
		}
		if a.SinceLast {
			since = lastReviewed(a.SinceMarks, a.Head, a.RequestBase, a.Cwd)
		}
		if since != "" {
			bases = append(bases, Base{"прошлый прогон", since})
		}
		var err error
		if baseSha, conflicted, err = virtualBase(bases, a.RequestBase, a.Cwd); err != nil {
			return nil, err
		}
	}
	diff, err := OverRange(baseSha, []string{baseSha, a.Head}, a.Cwd)
	if err != nil {
		return nil, err
	}
	kind := "task"
	switch {
	case a.ExplicitBase != "":
		kind = "explicit"
	case len(conflicted) > 0:
		kind = "mixed"
	}
	t := &TaskChanges{Kind: kind, Bases: bases, BaseSha: baseSha, Diff: diff, Conflicted: []string{}, Excluded: []string{}, Since: since}
	for _, p := range conflicted {
		if slices.Contains(diff.Files, p) {
			t.Conflicted = append(t.Conflicted, p)
		}
	}
	for _, f := range a.RequestDiff.Files {
		if !slices.Contains(diff.Files, f) {
			t.Excluded = append(t.Excluded, f)
		}
	}
	names := make([]string, len(bases))
	for i, b := range bases {
		names[i] = b.Rev + "@" + git.Short(b.Sha)
	}
	line := fmt.Sprintf("изменения задачи от %s: %d файлов, отсеяно чужих %d", strings.Join(names, ", "), len(diff.Files), len(t.Excluded))
	if len(t.Conflicted) > 0 {
		line += fmt.Sprintf(", конфликтных по диффу реквеста %d", len(t.Conflicted))
	}
	logx.Log(line)
	return t, nil
}
