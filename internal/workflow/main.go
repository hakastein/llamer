package workflow

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/runenv"
)

// Options — разобранные аргументы llamer run.
type Options struct{ Workflow, Context, Config string }

// projectConfig — конфиг проекта: --config или .llamer.json в корне репозитория.
func projectConfig(ctx context.Context, cwd, flag string, env map[string]string) (string, *config.Config, error) {
	file := runenv.Resolve(cwd, flag)
	if flag == "" {
		root, err := git.RepoRoot(ctx, cwd)
		if err != nil {
			return "", nil, &runenv.InputError{Message: "нет --config, а git не нашёл репозиторий в " + cwd + " (" + git.FirstLine(err) + ")"}
		}
		file = filepath.Join(root, config.File)
	}
	cfg, err := config.LoadShared(file, env)
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return "", nil, &runenv.InputError{Message: cfgErr.Message}
	}
	return file, cfg, err
}

// Main — llamer run: workflow на драйверах проекта в каталоге контекста.
// Временное прогона с копиями учётных данных удаляется при любом исходе.
func Main(ctx context.Context, o *Options, stdout io.Writer) (ok bool, err error) {
	vars, order := runenv.Environ()
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	file, cfg, err := projectConfig(ctx, cwd, o.Config, vars)
	if err != nil {
		return false, err
	}
	w, err := config.LoadWorkflow(runenv.Resolve(cwd, o.Workflow))
	var cfgErr *config.Error
	if errors.As(err, &cfgErr) {
		return false, &runenv.InputError{Message: cfgErr.Message}
	}
	if err != nil {
		return false, err
	}
	contextDir := runenv.Resolve(cwd, o.Context)
	if info, err := os.Stat(contextDir); err != nil || !info.IsDir() {
		return false, &runenv.InputError{Message: "--context " + o.Context + ": каталога нет"}
	}
	gaps := drivers.Gaps(ctx, w.Drivers(), cfg.Drivers, vars)
	// Пробел от проверки, сорванной сигналом, — не пробел окружения.
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if len(gaps) > 0 {
		return false, runenv.NotReady(gaps)
	}
	logx.Log("конфиг: " + cfg.Source + ", workflow: " + w.Dir)
	run, err := artifacts.Open(filepath.Dir(file), logx.StartedAt)
	if err != nil {
		return false, err
	}
	logx.Log("артефакты прогона: " + run.Path)
	env, err := runenv.Open(vars, order)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := env.Close(); closeErr != nil && err == nil {
			ok, err = false, closeErr
		}
	}()
	// Клиент брокера — под именем каждой команды верхнего уровня workflow.
	if w.Commands != nil {
		for _, c := range w.Commands.Commands {
			if err := env.Wrapper(c.Path[0], broker.Subcommand, c.Path[0]); err != nil {
				return false, err
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	prepared, err := env.Drivers(w.Drivers(), cfg.Drivers, w.Library, w.Skills(), run.Path, drivers.Calls{})
	if err != nil {
		return false, err
	}
	result, err := (&Run{Context: ctx, Workflow: w, Drivers: prepared, Artifacts: run, Cwd: contextDir,
		Out: stdout, Bin: env.Bin, ClientDir: filepath.Dir(exe)}).Execute()
	if err != nil {
		return false, err
	}
	return result.OK, nil
}
