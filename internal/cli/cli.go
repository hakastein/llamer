// Package cli — команда llamer: разбор аргументов, проверка окружения и прогон.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"al.essio.dev/pkg/shellescape"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/materials"
	"github.com/hakastein/llamer/internal/report"
	"github.com/hakastein/llamer/internal/stages"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

const Help = `llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.

Что разбирается:
  --mr <url|project!iid>      merge request в GitLab (нужен GITLAB_TOKEN; для project!iid — GITLAB_URL);
                              исключается целевая ветка MR — origin/<целевая> должна быть в репозитории
  --head <ревизия|worktree|index>
                              голова разбора: ревизия, рабочее дерево с неотслеживаемыми или индекс
  --base <ревизия>            разбор ровно от этой ревизии
  --exclude <ревизия>         всё, что достижимо из ревизии, в разбор не идёт; повторяемый, с --base не сочетается
  --task <файл>               текст задачи: этапы сверяют с ним реализацию

Без --mr нужны --head и ровно одно из --base или --exclude; с --mr --head и --base не задаются.

Выход:
  по умолчанию                YAML-результат в stdout и в .llamer/<прогон>/result.yaml, отчёт report.md рядом
  --publish gitlab            треды, заметка о рисках, сводка и статус в MR (только с --mr)
  --dry-run                   ничего не публиковать: отчёт вместо --publish (только с --mr)
  --since-last                только изменения после прошлого прогона LLaMer в MR (только с --mr)

Прочее:
  --config <файл>             конфиг вместо .llamer.json в корне репозитория; .llamer.local.json рядом с ним накладывается
  --preflight                 проверить окружение и напечатать YAML пробелов, прогон не начинать
  -h, --help                  эта справка
`

// Options — разобранные аргументы llamer.
type Options struct {
	Publish, DryRun, SinceLast, Preflight bool
	Input                                 string // gitlab или revisions
	MR, Base, Head, Task, Config          string
	PublishTo                             string
	Exclude                               []string
}

func usage(message string) error { return &InputError{message} }

// check — сочетания флагов; set — задан ли флаг.
func (o *Options) check(set func(string) bool) error {
	mr := set("mr")
	switch {
	case mr && (set("head") || set("base")):
		return usage("с --mr база и голова берутся из merge request: --head и --base не задаются")
	case !mr && !set("head"):
		return usage("нужен --mr или --head — справка: llamer --help")
	case set("base") && len(o.Exclude) > 0:
		return usage("--base и --exclude не сочетаются: нужно одно из них")
	case !mr && !set("base") && len(o.Exclude) == 0:
		return usage("с --head нужно одно из --base или --exclude — справка: llamer --help")
	case set("publish") && o.PublishTo != "gitlab":
		return usage("--publish: поддерживается только gitlab; пришло «" + o.PublishTo + "»")
	}
	for _, flag := range []string{"publish", "dry-run", "since-last"} {
		if set(flag) && !mr {
			return usage("--" + flag + " — только вместе с --mr")
		}
	}
	o.Input = "revisions"
	if mr {
		o.Input = "gitlab"
	}
	o.Publish = set("publish") && !o.DryRun
	return nil
}

type gap = drivers.Gap

// ready — итог проверки окружения.
type ready struct {
	root    string
	config  *config.Config
	library *stages.Library
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

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// checkReadiness — проверяет только своё: git, драйверы этапов и их вход, токен
// GitLab при GitLab-входе и наличие скиллов. Софт скиллов, навигацию и docker
// готовит запускающий. Любой пробел останавливает прогон.
func checkReadiness(cwd string, o *Options, env map[string]string) (*ready, error) {
	root, err := git.RepoRoot(cwd)
	if err != nil {
		return &ready{gaps: []gap{{ID: "git", Message: "git не нашёл репозиторий в " + cwd + " (" + git.FirstLine(err) + ")"}}}, nil
	}
	file := filepath.Join(root, config.File)
	if o.Config != "" {
		file = resolvePath(cwd, o.Config)
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
	for _, name := range cfg.DriversInUse() {
		r.gaps = append(r.gaps, drivers.Gaps(name, cfg.Drivers[name], env)...)
	}
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

func formatGaps(gaps []gap) string {
	lines := make([]string, len(gaps))
	for i, g := range gaps {
		lines[i] = "- " + g.Message
	}
	return strings.Join(lines, "\n")
}

// Что убрать, если прогон прервут: этапы с моделями жили бы дальше, копии
// кредов и выложенная голова остались бы.
var (
	interruptMu sync.Mutex
	onInterrupt []func()
)

func pushCleanup(f func()) {
	interruptMu.Lock()
	defer interruptMu.Unlock()
	onInterrupt = append(onInterrupt, f)
}

func watchSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-signals
		name := map[os.Signal]string{syscall.SIGINT: "SIGINT", syscall.SIGTERM: "SIGTERM"}[sig]
		logx.Logf("прогон прерван: %s — этапы остановлены", name)
		agent.StopStages()
		interruptMu.Lock()
		for i := len(onInterrupt) - 1; i >= 0; i-- {
			onInterrupt[i]()
		}
		os.Exit(128 + int(sig.(syscall.Signal)))
	}()
}

// HarnessCommand — скрытая подкоманда llamer, которой обёртка `review` этапа зовёт харнесс.
const HarnessCommand = "__harness"

// prepareStageBin — каталог команд этапа: обёртка `review` над тем же
// бинарником, что ведёт прогон. Пишется на каждый прогон: `review` в PATH
// этапа мог бы оказаться чужим.
func prepareStageBin(dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	script := "#!/bin/sh\nexec " + shellescape.Quote(exe) + " " + HarnessCommand + " \"$@\"\n"
	path := filepath.Join(dir, harness.Name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return dir, os.Chmod(path, 0o755)
}

func readTask(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, _ := filepath.Abs(path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", &InputError{"--task " + path + ": файл не читается (" + err.Error() + ")"}
	}
	return strings.TrimSpace(string(data)), nil
}

type run struct {
	options   *Options
	ready     *ready
	env       map[string]string
	envOrder  []string
	artifacts *artifacts.Run
	tmp       string
	issue     string
	stdout    io.Writer
}

func (r *run) openRequest() (request, error) {
	o := r.options
	if o.Input == "gitlab" {
		return openGitlab(o.MR, r.env, r.ready.root, o.Exclude)
	}
	return openRevisions(r.ready.root, o.Base, o.Head, o.Exclude)
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
	t, err := openTree(req, r.ready.root)
	if err != nil {
		return false, err
	}
	pushCleanup(t.close)
	defer t.close()

	scope, err := materials.PlanReviewScope(req, t.path, r.options.SinceLast)
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

	m, err := materials.Gather(req, t.path, scope, r.issue, r.artifacts.SaveDiff)
	if err != nil {
		return false, err
	}
	// Драйвер получает пакет как есть, а в артефакты ложится копия с вымаранными секретами.
	r.artifacts.SavePrompt("общий пакет", m.SharedPack())
	shared := &drivers.Shared{Text: m.SharedPack(), Path: filepath.Join(r.tmp, "request-materials.md")}
	if err := os.WriteFile(shared.Path, []byte(shared.Text), 0o666); err != nil {
		return false, err
	}
	runSkills := ""
	if skills := cfg.SkillsInUse(); len(skills) > 0 {
		if runSkills, err = library.Materialize(filepath.Join(r.artifacts.Path, "stage-plugin"), skills); err != nil {
			return false, err
		}
	}
	stageBin, err := prepareStageBin(filepath.Join(r.tmp, "bin"))
	if err != nil {
		return false, err
	}
	prepared := map[string]drivers.Driver{}
	for _, name := range cfg.DriversInUse() {
		d, err := drivers.Prepare(name, drivers.Setup{Settings: cfg.Drivers[name], Env: r.env, EnvOrder: r.envOrder,
			StageBin: stageBin, RunSkills: runSkills, Tmp: r.tmp})
		if err != nil {
			return false, err
		}
		prepared[name] = d
	}
	outcome, err := stages.Run(&stages.Toolkit{Artifacts: r.artifacts, Drivers: prepared, Materials: m, Library: library,
		Config: cfg, Shared: shared})
	if err != nil {
		return false, err
	}

	if !outcome.OK() {
		detail := "оценщик не отдал вердикт: неизвестна глубина разбора"
		logx.Log("прогон не удался: " + detail)
		if found := len(outcome.Journal.Surviving()); found > 0 {
			logx.Logf("найдено до срыва: %d — остались в журнале прогона", found)
		}
		f := report.Failure{Reason: "other", Detail: detail}
		if outcome.Failure != nil {
			f.Reason, f.ResetsAt = outcome.Failure.Reason, agent.ResetsAt(outcome.Failure.RateLimit)
		}
		delivery.DeliverFailure(f, outcome.Records)
		r.artifacts.ReportCost(outcome.Records, logx.SinceStart())
		return false, nil
	}

	review := &report.Review{Journal: outcome.Journal, Materials: m, Stages: outcome.Records, MarkSha: req.HeadSha(), ElapsedMs: logx.SinceStart()}
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
	r.artifacts.ReportCost(outcome.Records, logx.SinceStart())
	// Сорванный этап — неполное ревью, даже если отчёт доставлен.
	return len(failures) == 0 && shortfall.Failed == 0, nil
}

func environ() (map[string]string, []string) {
	env := map[string]string{}
	var order []string
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if _, seen := env[name]; !seen {
			order = append(order, name)
		}
		env[name] = value
	}
	return env, order
}

func main(options *Options, stdout io.Writer) (bool, error) {
	env, order := environ()
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	rd, err := checkReadiness(cwd, options, env)
	if err != nil {
		return false, err
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
		return false, &InputError{"прогон не начат — окружение не готово:\n" + formatGaps(rd.gaps)}
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
	// Временное прогона: обёртка харнесса, пакет, приватные конфиги драйверов с
	// копией кредов — после прогона не должно остаться ничего.
	tmp, err := os.MkdirTemp("", "llamer-run-")
	if err != nil {
		return false, err
	}
	var once sync.Once
	var cleanErr error
	cleanUp := func() {
		once.Do(func() {
			if err := os.RemoveAll(tmp); err != nil {
				cleanErr = fmt.Errorf("временный каталог с копиями учётных данных не удалён: %w", err)
				logx.Log(cleanErr.Error())
			}
			if err := runArtifacts.RedactJournals(); err != nil {
				cleanErr = errors.Join(cleanErr, fmt.Errorf("журналы прогона не вымараны: %w", err))
				logx.Logf("журналы прогона не вымараны (%s) — в артефактах могут остаться секреты", err)
			}
			logx.Log("артефакты прогона: " + runArtifacts.Path)
		})
	}
	pushCleanup(cleanUp)
	defer cleanUp()
	ok, err = (&run{options: options, ready: rd, env: env, envOrder: order, artifacts: runArtifacts, tmp: tmp, issue: issue, stdout: stdout}).review()
	cleanUp()
	if err == nil && cleanErr != nil {
		return false, cleanErr
	}
	return ok, err
}

// run — прогон llamer; возвращает код выхода.
func runMain(options *Options, stdout io.Writer) (code int) {
	watchSignals()
	defer func() {
		if p := recover(); p != nil {
			code = fail(fmt.Sprint(p))
		}
	}()
	ok, err := main(options, stdout)
	// Прерванный прогон завершает обработчик сигнала, своим кодом: здесь его ждём.
	interruptMu.Lock()
	defer interruptMu.Unlock()
	if err != nil {
		return fail(err.Error())
	}
	if ok {
		return 0
	}
	return 1
}

func fail(message string) int {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		lines[i] = "[llamer] " + line
	}
	logx.Stamped(strings.Join(lines, "\n"))
	return 1
}

// Execute — команда llamer со скрытыми подкомандами харнесса этапа и хука
// claude: этап зовёт ровно ту версию, что ведёт прогон. Возвращает код выхода.
func Execute(argv []string, stdout io.Writer) int {
	code := 0
	o := &Options{}
	root := &cobra.Command{Use: "llamer", Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.check(cmd.Flags().Changed); err != nil {
				return err
			}
			code = runMain(o, stdout)
			return nil
		}}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &InputError{harness.FlagError(err) + " — справка: llamer --help"}
	})
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &InputError{harness.FlagError(err) + " — справка: llamer --help"}
	})
	root.SetHelpFunc(func(*cobra.Command, []string) { io.WriteString(stdout, Help) })
	f := root.Flags()
	f.StringVar(&o.MR, "mr", "", "")
	f.StringVar(&o.Base, "base", "", "")
	f.StringVar(&o.Head, "head", "", "")
	f.StringArrayVar(&o.Exclude, "exclude", nil, "")
	f.StringVar(&o.Task, "task", "", "")
	f.StringVar(&o.PublishTo, "publish", "", "")
	f.BoolVar(&o.DryRun, "dry-run", false, "")
	f.BoolVar(&o.SinceLast, "since-last", false, "")
	f.StringVar(&o.Config, "config", "", "")
	f.BoolVar(&o.Preflight, "preflight", false, "")
	root.AddCommand(
		&cobra.Command{Use: HarnessCommand, Hidden: true, DisableFlagParsing: true,
			Run: func(_ *cobra.Command, args []string) { code = harness.Main(args, os.Stdout, os.Stderr) }},
		&cobra.Command{Use: drivers.ClaudeHook, Hidden: true,
			RunE: func(*cobra.Command, []string) error { return drivers.HookMain(os.Stdin, os.Stdout) }},
	)
	root.SetArgs(argv)
	root.SetOut(os.Stderr)
	root.SetErr(os.Stderr)
	if err := root.Execute(); err != nil {
		var input *InputError
		if !errors.As(err, &input) {
			err = &InputError{err.Error() + " — справка: llamer --help"}
		}
		return fail(err.Error())
	}
	return code
}
