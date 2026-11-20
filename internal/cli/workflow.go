package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/workflow"
	"github.com/spf13/cobra"
)

const RunHelp = `llamer run <каталог workflow> — этапы workflow по порядку его workflow.json.

  --context <каталог>         рабочий каталог агента с подготовленным контекстом; по умолчанию текущий
  --config <файл>             конфиг проекта вместо .llamer.json в корне репозитория
  -h, --help                  эта справка

Выход: строки JSON в stdout — события этапов, последней — результат; результат ещё и в
.llamer/<прогон>/result.json рядом с конфигом проекта.
`

// RunOptions — разобранные аргументы llamer run.
type RunOptions struct{ Workflow, Context, Config string }

func runCommand(stdout io.Writer, code *int) *cobra.Command {
	o := &RunOptions{}
	cmd := &cobra.Command{Use: "run", SilenceErrors: true, SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usage("нужен ровно один каталог workflow — справка: llamer run --help")
			}
			o.Workflow = args[0]
			*code = runMain(func(ctx context.Context) (bool, error) { return runWorkflow(ctx, o, stdout) })
			return nil
		}}
	cmd.SetHelpFunc(func(*cobra.Command, []string) { io.WriteString(stdout, RunHelp) })
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &InputError{harness.FlagError(err) + " — справка: llamer run --help"}
	})
	f := cmd.Flags()
	f.StringVar(&o.Context, "context", ".", "")
	f.StringVar(&o.Config, "config", "", "")
	return cmd
}

// projectConfig — конфиг проекта: --config или .llamer.json в корне репозитория.
func projectConfig(ctx context.Context, cwd, flag string, env map[string]string) (string, *config.Config, error) {
	file := resolvePath(cwd, flag)
	if flag == "" {
		root, err := git.RepoRoot(ctx, cwd)
		if err != nil {
			return "", nil, &InputError{"нет --config, а git не нашёл репозиторий в " + cwd + " (" + git.FirstLine(err) + ")"}
		}
		file = filepath.Join(root, config.File)
	}
	cfg, err := config.LoadShared(file, env)
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return "", nil, &InputError{cfgErr.Message}
	}
	return file, cfg, err
}

// runWorkflow — llamer run: workflow на драйверах проекта в каталоге контекста.
// Временное прогона с копиями учётных данных удаляется при любом исходе.
func runWorkflow(ctx context.Context, o *RunOptions, stdout io.Writer) (ok bool, err error) {
	env, order := environ()
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	file, cfg, err := projectConfig(ctx, cwd, o.Config, env)
	if err != nil {
		return false, err
	}
	w, err := config.LoadWorkflow(resolvePath(cwd, o.Workflow))
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return false, &InputError{cfgErr.Message}
	}
	if err != nil {
		return false, err
	}
	contextDir := resolvePath(cwd, o.Context)
	if info, err := os.Stat(contextDir); err != nil || !info.IsDir() {
		return false, &InputError{"--context " + o.Context + ": каталога нет"}
	}
	var gaps []gap
	for _, name := range w.Drivers() {
		gaps = append(gaps, drivers.Gaps(ctx, name, cfg.Drivers[name], env)...)
	}
	// Пробел от проверки, сорванной сигналом, — не пробел окружения.
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if len(gaps) > 0 {
		return false, &InputError{"прогон не начат — окружение не готово:\n" + formatGaps(gaps)}
	}
	logx.Log("конфиг: " + cfg.Source + ", workflow: " + w.Dir)
	run, err := artifacts.Open(filepath.Dir(file), logx.StartedAt)
	if err != nil {
		return false, err
	}
	logx.Log("артефакты прогона: " + run.Path)
	tmp, err := os.MkdirTemp("", "llamer-run-")
	if err != nil {
		return false, err
	}
	defer func() {
		if rmErr := os.RemoveAll(tmp); rmErr != nil {
			rmErr = fmt.Errorf("временный каталог с копиями учётных данных не удалён: %w", rmErr)
			logx.Log(rmErr.Error())
			if err == nil {
				ok, err = false, rmErr
			}
		}
	}()
	// Каталог команд этапа первым в PATH: пустой, но не пустая строка — та значила бы текущий каталог.
	stageBin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(stageBin, 0o777); err != nil {
		return false, err
	}
	prepared, err := prepareDrivers(w.Drivers(), cfg.Drivers, w.Library, w.Skills(), run.Path,
		drivers.Setup{Env: env, EnvOrder: order, StageBin: stageBin, Tmp: tmp})
	if err != nil {
		return false, err
	}
	result, err := (&workflow.Run{Context: ctx, Workflow: w, Drivers: prepared, Artifacts: run, Cwd: contextDir,
		Out: stdout}).Execute()
	if err != nil {
		return false, err
	}
	return result.OK, nil
}
