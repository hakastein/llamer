// Package review — прогон ревью: проверка окружения, вход, материалы, этапы и доставка результата.
package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/materials"
	"github.com/hakastein/llamer/internal/report"
	artifacts "github.com/hakastein/llamer/internal/reviewartifacts"
	"github.com/hakastein/llamer/internal/runenv"
	"github.com/hakastein/llamer/internal/stages"
	"github.com/hakastein/smith/cost"
	"github.com/hakastein/smith/drivers"
	"github.com/hakastein/smith/logx"
	"github.com/hakastein/smith/skills"
	"go.yaml.in/yaml/v3"
)

// Options — разобранные аргументы ревью.
type Options struct {
	Publish, DryRun, SinceLast, Preflight bool
	Input                                 string // gitlab или revisions
	MR, Base, Head, Task, Config          string
	PublishTo                             string
	Exclude                               []string
}

type gap = drivers.Gap

// ready — итог проверки окружения.
type ready struct {
	root    string
	config  *config.Config
	library *skills.Library
	gaps    []gap
}

func gitlabGaps(o *Options, env map[string]string) []gap {
	var gaps []gap
	if env["GITLAB_TOKEN"] == "" {
		gaps = append(gaps, gap{ID: "gitlab-token", Message: "не задан GITLAB_TOKEN — merge request не прочитать и не опубликовать"})
	}
	if ref, err := gitlab.ParseRef(o.MR, env); err != nil {
		gaps = append(gaps, gap{ID: "mr-ref", Message: err.Error()})
	} else if ref.URL == "" {
		gaps = append(gaps, gap{ID: "gitlab-url", Message: "адрес GitLab неизвестен: дай ссылку на MR целиком или задай " + strings.Join(gitlab.URLEnv, " или ")})
	}
	return gaps
}

// checkReadiness — проверяет только своё: git, драйверы этапов и их вход, токен
// GitLab при GitLab-входе и наличие скиллов. Софт скиллов, навигацию и docker
// готовит запускающий. Любой пробел останавливает прогон.
func checkReadiness(ctx context.Context, cwd string, o *Options, env map[string]string) (*ready, error) {
	root, err := git.RepoRoot(ctx, cwd)
	if err != nil {
		return &ready{gaps: []gap{{ID: "git", Message: "git не нашёл репозиторий в " + cwd + " (" + git.FirstLine(err) + ")"}}}, nil
	}
	file := filepath.Join(root, config.File)
	if o.Config != "" {
		file = runenv.Resolve(cwd, o.Config)
	}
	cfg, err := config.Load(file, env)
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) {
			return nil, err
		}
		return &ready{root: root, gaps: []gap{{ID: "config", Message: cfgErr.Message}}}, nil
	}
	r := &ready{root: root, config: cfg, gaps: []gap{}}
	r.gaps = append(r.gaps, drivers.Gaps(ctx, cfg.DriversInUse(), cfg.Drivers, env)...)
	if o.Input == "gitlab" {
		r.gaps = append(r.gaps, gitlabGaps(o, env)...)
	}
	library, err := stages.LoadLibrary(cfg.SkillPaths)
	if err != nil {
		r.gaps = append(r.gaps, gap{ID: "skills", Message: err.Error()})
	} else {
		r.library = library
		if missing := library.Missing(cfg.SkillsInUse()); len(missing) > 0 {
			paths := strings.Join(cfg.SkillPaths, ", ")
			if paths == "" {
				paths = "пусто"
			}
			r.gaps = append(r.gaps, gap{ID: "skills", Message: "скиллы не найдены в skillPaths (" + paths + "): " + strings.Join(missing, ", ")})
		}
	}
	return r, nil
}

func readTask(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, _ := filepath.Abs(path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", &runenv.InputError{Message: "--task " + path + ": файл не читается (" + err.Error() + ")"}
	}
	return strings.TrimSpace(string(data)), nil
}

type run struct {
	ctx       context.Context
	options   *Options
	ready     *ready
	env       *runenv.Env
	artifacts *artifacts.Run
	issue     string
	stdout    io.Writer
}

func (r *run) openRequest() (request, error) {
	o := r.options
	if o.Input == "gitlab" {
		return openGitlab(r.ctx, o.MR, r.env.Vars, r.ready.root, o.Exclude)
	}
	return openRevisions(r.ctx, r.ready.root, o.Base, o.Head, o.Exclude)
}

func (r *run) review() (bool, error) {
	cfg, library := r.ready.config, r.ready.library
	req, err := r.openRequest()
	if err != nil {
		return false, err
	}
	var delivery report.Delivery
	if r.options.Publish {
		delivery = &report.GitlabDelivery{MR: req.(*gitlabRequest).mr, RunPath: r.artifacts.Path}
	} else {
		delivery = &report.ReportDelivery{Request: req, Artifacts: r.artifacts, Stdout: r.stdout}
	}
	t, err := openTree(r.ctx, req, r.ready.root)
	if err != nil {
		return false, err
	}
	defer t.close()

	scope, err := materials.PlanReviewScope(r.ctx, req, t.path, r.options.SinceLast)
	// Прерванный прогон результата не выдаёт: отказ git от отмены — не срыв разбора.
	if r.ctx.Err() != nil {
		return false, r.ctx.Err()
	}
	if err != nil {
		var scopeErr *materials.TaskChangesError
		if !errors.As(err, &scopeErr) {
			return false, err
		}
		// Дифф реквеста вместо изменений задачи — разбор чужого кода, ради отказа
		// от которого они и считаются: без них ревью не идёт вовсе.
		logx.Log("изменения задачи не вычислены: " + scopeErr.Message + " — ревью не запускается")
		delivery.DeliverFailure(report.Failure{Reason: "scope", Detail: scopeErr.Message}, nil)
		return false, nil
	}
	if scope.NothingOwn {
		logx.Log("разбирать нечего: голова не добавляет изменений к базе — этапы не поднимаются")
		delivery.DeliverNothingOwn(scope.Task, req.HeadSha())
		return true, nil
	}

	m, err := materials.Gather(r.ctx, req, t.path, scope, r.issue, r.artifacts.SaveDiff)
	if err != nil {
		return false, err
	}
	// Драйвер получает пакет как есть, а в артефакты ложится копия с вымаранными секретами.
	r.artifacts.SavePrompt("общий пакет", m.SharedPack())
	shared := filepath.Join(r.env.Tmp, "request-materials.md")
	if err := os.WriteFile(shared, []byte(m.SharedPack()), 0o666); err != nil {
		return false, err
	}
	if err := r.env.Wrapper(harness.Name, harness.Subcommand); err != nil {
		return false, err
	}
	prepared, err := r.env.Drivers(cfg.DriversInUse(), cfg.Drivers, library, cfg.SkillsInUse(), r.artifacts.Path)
	if err != nil {
		return false, err
	}
	outcome, err := stages.Run(&stages.Toolkit{Context: r.ctx, Artifacts: r.artifacts, Drivers: prepared, Materials: m, Library: library,
		Config: cfg, Shared: shared})
	if err != nil {
		return false, err
	}
	// Отменённые этапы — не итог ревью: ни результата, ни публикации.
	if r.ctx.Err() != nil {
		return false, r.ctx.Err()
	}

	if !outcome.OK() {
		detail := "оценщик не отдал вердикт: неизвестна глубина разбора"
		logx.Log("прогон не удался: " + detail)
		if found := len(outcome.Journal.Surviving()); found > 0 {
			logx.Logf("найдено до срыва: %d — остались в журнале прогона", found)
		}
		f := report.Failure{Reason: "other", Detail: detail}
		if outcome.Failure != nil {
			f.Reason, f.ResetsAt = outcome.Failure.Reason, outcome.Failure.Outcome.ResetsAt
		}
		delivery.DeliverFailure(f, outcome.Records)
		cost.Report(r.artifacts.Path, outcome.Records, logx.SinceStart())
		return false, nil
	}

	review := &report.Review{Journal: outcome.Journal, Materials: m, Stages: outcome.Records, Done: outcome.Done, MarkSha: req.HeadSha(), ElapsedMs: logx.SinceStart()}
	unpublished, failures := delivery.Deliver(review)
	for _, line := range review.Repeats() {
		logx.Log(line)
	}
	for _, line := range report.CollectDropped(outcome.Journal.Findings) {
		logx.Log(line)
	}
	shortfall := review.Shortfall(unpublished)
	for _, notice := range shortfall.Notices {
		logx.Log(notice)
	}
	for _, g := range shortfall.Gaps {
		logx.Log("недостача: " + g)
	}
	cost.Report(r.artifacts.Path, outcome.Records, logx.SinceStart())
	// Сорванный этап — неполное ревью, даже если отчёт доставлен.
	return len(failures) == 0 && shortfall.Failed == 0, nil
}

// Main — ревью или, с Preflight, только проверка окружения; ok — ревью полное или окружение готово.
func Main(ctx context.Context, options *Options, stdout io.Writer) (bool, error) {
	vars, order := runenv.Environ()
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	rd, err := checkReadiness(ctx, cwd, options, vars)
	if err != nil {
		return false, err
	}
	// Пробел от проверки, сорванной сигналом, — не пробел окружения.
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	ok := len(rd.gaps) == 0
	if options.Preflight {
		enc := yaml.NewEncoder(stdout)
		enc.SetIndent(2)
		return ok, enc.Encode(struct {
			Version int   `yaml:"version"`
			OK      bool  `yaml:"ok"`
			Gaps    []gap `yaml:"gaps"`
		}{report.Version, ok, rd.gaps})
	}
	if !ok {
		return false, runenv.NotReady(rd.gaps)
	}
	logx.Log("конфиг: " + rd.config.Source)
	issue, err := readTask(options.Task)
	if err != nil {
		return false, err
	}
	runArtifacts, err := artifacts.Open(rd.root, logx.StartedAt)
	if err != nil {
		return false, err
	}
	logx.Log("артефакты прогона: " + runArtifacts.Path)
	env, err := runenv.Open(vars, order)
	if err != nil {
		return false, err
	}
	var once sync.Once
	var cleanErr error
	cleanUp := func() {
		once.Do(func() {
			cleanErr = env.Close()
			if err := runArtifacts.RedactJournals(); err != nil {
				cleanErr = errors.Join(cleanErr, fmt.Errorf("журналы прогона не вымараны: %w", err))
				logx.Logf("журналы прогона не вымараны (%s) — в артефактах могут остаться секреты", err)
			}
			logx.Log("артефакты прогона: " + runArtifacts.Path)
		})
	}
	defer cleanUp()
	ok, err = (&run{ctx: ctx, options: options, ready: rd, env: env, artifacts: runArtifacts, issue: issue, stdout: stdout}).review()
	cleanUp()
	if err == nil && cleanErr != nil {
		return false, cleanErr
	}
	return ok, err
}
