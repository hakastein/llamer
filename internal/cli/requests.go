package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/materials"
	gl "gitlab.com/gitlab-org/api/client-go"
)

// InputError — вход прогона не годится для разбора: причину читает человек, а не стек.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func resolveRevision(rev, flag, cwd string) (string, error) {
	sha, ok := git.Try([]string{"rev-parse", "--verify", "--quiet", rev + "^{commit}"}, cwd)
	if !ok || sha == "" {
		return "", &InputError{flag + " " + rev + ": такой ревизии в репозитории нет"}
	}
	return sha, nil
}

// request — вход прогона: MR в GitLab или ревизии репозитория.
type request interface {
	materials.Request
	Label() string
	workTree() bool
}

// gitlabRequest — merge request в GitLab: `--mr`. Треды и публикация — только у
// него. Из разбора исключается целевая ветка в том виде, в каком она лежит в
// origin, и `--exclude`.
type gitlabRequest struct {
	mr       *gitlab.MergeRequest
	info     *gl.MergeRequest
	excludes []string
}

func openGitlab(ref string, env map[string]string, cwd string, exclude []string) (*gitlabRequest, error) {
	r, err := gitlab.ParseRef(ref, env)
	if err != nil {
		return nil, err
	}
	mr, err := gitlab.NewMergeRequest(r, env["GITLAB_TOKEN"])
	if err != nil {
		return nil, err
	}
	info, err := mr.Info()
	if err != nil {
		return nil, err
	}
	logx.Logf("MR !%s «%s» (%s → %s)", r.IID, info.Title, info.SourceBranch, info.TargetBranch)
	if info.DiffRefs.BaseSha == "" || info.DiffRefs.HeadSha == "" {
		return nil, &InputError{"у MR !" + r.IID + " нет diff_refs — разбирать нечего"}
	}
	excludes := append([]string{"origin/" + info.TargetBranch}, exclude...)
	if _, err := resolveRevision(excludes[0], "целевая ветка MR", cwd); err != nil {
		return nil, err
	}
	for _, rev := range exclude {
		if _, err := resolveRevision(rev, "--exclude", cwd); err != nil {
			return nil, err
		}
	}
	return &gitlabRequest{mr, info, excludes}, nil
}

func (g *gitlabRequest) Kind() string         { return "gitlab" }
func (g *gitlabRequest) BaseSha() string      { return g.info.DiffRefs.BaseSha }
func (g *gitlabRequest) HeadSha() string      { return g.info.DiffRefs.HeadSha }
func (g *gitlabRequest) Excludes() []string   { return g.excludes }
func (g *gitlabRequest) ExplicitBase() string { return "" }
func (g *gitlabRequest) workTree() bool       { return false }
func (g *gitlabRequest) Label() string {
	return fmt.Sprintf("MR !%d %s → %s", g.mr.IID, g.info.SourceBranch, g.info.TargetBranch)
}

// RequestDiff — GitLab сводит ветку с целевой сам; локальный git diff от базы
// MR на ветке с влитой целевой тянет чужие правки.
func (g *gitlabRequest) RequestDiff(cwd string) (*materials.Diff, error) {
	files, err := g.mr.MergeDiff()
	if err != nil {
		logx.Log("дифф у GitLab не спросился (" + strings.Split(err.Error(), "\n")[0] + ") — считаем локально от базы MR")
		return materials.OverRange(g.BaseSha(), []string{g.BaseSha(), g.HeadSha()}, cwd)
	}
	logx.Logf("дифф реквеста от GitLab: %d файлов", len(files))
	return materials.FromMergeDiff(files, g.BaseSha(), cwd)
}

func (g *gitlabRequest) Discussions() ([]*gl.Discussion, error) { return g.mr.Discussions() }

// snapshot — снимок коммитом поверх HEAD: индекса (`index`) или рабочего дерева
// с неотслеживаемыми (`worktree`). Индекс копируется во временный файл, чтобы ни
// `git add -A`, ни `write-tree` не трогали индекс человека. Копия сохраняет время
// изменения: по нему git узнаёт записи, изменённые в ту же секунду, что и
// индекс, и перечитывает их — со свежим временем правка той же длины потерялась бы.
func snapshot(cwd, what string) (string, error) {
	dir, err := os.MkdirTemp("", "llamer-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	index := filepath.Join(dir, "index")
	gitPath, err := git.Git([]string{"rev-parse", "--git-path", "index"}, cwd)
	if err != nil {
		return "", err
	}
	own := gitPath
	if !filepath.IsAbs(own) {
		own = filepath.Join(cwd, own)
	}
	if info, err := os.Stat(own); err == nil {
		data, err := os.ReadFile(own)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(index, data, info.Mode().Perm()); err != nil {
			return "", err
		}
		if err := os.Chtimes(index, info.ModTime(), info.ModTime()); err != nil {
			return "", err
		}
	}
	env := append(slices.Clone(materials.MachineAuthor), "GIT_INDEX_FILE="+index)
	if what == "worktree" {
		if _, err := git.Git([]string{"add", "-A", "--", ":/"}, cwd, env...); err != nil {
			return "", err
		}
	}
	tree, err := git.Git([]string{"write-tree"}, cwd, env...)
	if err != nil {
		return "", err
	}
	label := "индекса"
	if what == "worktree" {
		label = "рабочего дерева"
	}
	return git.Git([]string{"commit-tree", tree, "-p", "HEAD", "-m", "llamer: снимок " + label}, cwd, env...)
}

// revisionRequest — ревизии репозитория: `--head` и `--base` или `--exclude`.
// С `--base` сравнение — от неё как есть; с `--exclude` дифф реквеста
// считается от точки расхождения с первой исключённой ревизией, а разбор — от всех.
type revisionRequest struct {
	head, base       string
	exclude          []string
	headSha, baseSha string
}

func openRevisions(cwd, base, head string, exclude []string) (*revisionRequest, error) {
	var headSha string
	var err error
	if head == "worktree" || head == "index" {
		headSha, err = snapshot(cwd, head)
	} else {
		headSha, err = resolveRevision(head, "--head", cwd)
	}
	if err != nil {
		return nil, err
	}
	for _, rev := range exclude {
		if _, err := resolveRevision(rev, "--exclude", cwd); err != nil {
			return nil, err
		}
	}
	var baseSha string
	if base != "" {
		if baseSha, err = resolveRevision(base, "--base", cwd); err != nil {
			return nil, err
		}
	} else {
		sha, ok := git.Try([]string{"merge-base", headSha, exclude[0]}, cwd)
		if !ok || sha == "" {
			return nil, &InputError{git.NoCommonHistory(headSha, exclude[0], cwd)}
		}
		baseSha = sha
	}
	from := "без " + strings.Join(exclude, ", ")
	if base != "" {
		from = "база " + base
	}
	logx.Logf("голова %s (%s), %s", head, git.Short(headSha), from)
	return &revisionRequest{head, base, exclude, headSha, baseSha}, nil
}

func (r *revisionRequest) Kind() string       { return "revisions" }
func (r *revisionRequest) BaseSha() string    { return r.baseSha }
func (r *revisionRequest) HeadSha() string    { return r.headSha }
func (r *revisionRequest) Excludes() []string { return r.exclude }
func (r *revisionRequest) ExplicitBase() string {
	if r.base != "" {
		return r.baseSha
	}
	return ""
}

// workTree — голова — само рабочее дерево: этапы ходят по нему, отдельная выкладка не нужна.
func (r *revisionRequest) workTree() bool { return r.head == "worktree" }

func (r *revisionRequest) Label() string {
	if r.base != "" {
		return r.base + ".." + r.head
	}
	return r.head + " без " + strings.Join(r.exclude, ", ")
}

func (r *revisionRequest) RequestDiff(cwd string) (*materials.Diff, error) {
	return materials.OverRange(r.baseSha, []string{r.baseSha, r.headSha}, cwd)
}

func (r *revisionRequest) Discussions() ([]*gl.Discussion, error) { return nil, nil }

// tree — дерево, по которому ходят этапы, — с файлами головы разбора.
type tree struct {
	path  string
	close func()
}

// openTree — рабочая копия годится, если стоит на голове; иначе голова
// выкладывается отдельным worktree во временный каталог (объекты общие с
// основным `.git`) и убирается после прогона.
func openTree(r request, root string) (*tree, error) {
	if head, err := git.CurrentHead(root); r.workTree() || (err == nil && head == r.HeadSha()) {
		return &tree{root, func() {}}, nil
	}
	if _, ok := git.Try([]string{"cat-file", "-e", r.HeadSha() + "^{commit}"}, root); !ok {
		return nil, &InputError{"коммита " + git.Short(r.HeadSha()) + " в репозитории нет — сделай git fetch"}
	}
	path, err := os.MkdirTemp("", "llamer-tree-")
	if err != nil {
		return nil, err
	}
	// Без hooksPath post-checkout-хук (lfs и подобные) может вернуть ненулевой
	// код, и выкладка сочтётся упавшей.
	if _, err := git.Git([]string{"-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", path, r.HeadSha()}, root); err != nil {
		return nil, err
	}
	logx.Logf("голова %s выложена в %s", git.Short(r.HeadSha()), path)
	return &tree{path, func() {
		git.Try([]string{"worktree", "remove", "--force", path}, root)
		os.RemoveAll(path)
	}}, nil
}
