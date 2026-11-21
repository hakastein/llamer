package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/runenv"
	"github.com/spf13/cobra"
)

// Options — разобранные аргументы llamer run.
type Options struct {
	Dir, Context, Config string
	DryRun               bool
	Args                 []string // имя workflow и его аргументы
}

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

// Main — llamer run: workflow из каталога llamer проекта с аргументами o.Args. Справка workflow — тоже здесь:
// её печатает команда, собранная cliche из его объявления.
func Main(ctx context.Context, o *Options, stdout io.Writer) (ok bool, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	dir, err := FindDir(cwd, o.Dir, os.Getenv(DirEnv))
	if err != nil {
		return false, err
	}
	p, err := LoadProject(dir)
	if err != nil {
		return false, err
	}
	// Справку и ошибки команда workflow подписывает своим путём: llamer run <workflow>.
	root := &cobra.Command{Use: "llamer", SilenceErrors: true, SilenceUsage: true}
	root.CompletionOptions.DisableDefaultCmd = true
	runCmd := &cobra.Command{Use: "run", Args: cobra.ArbitraryArgs, RunE: func(_ *cobra.Command, args []string) error {
		return fmt.Errorf("в %s нет workflow %s", filepath.Join(dir, "workflows"), strings.Join(args, " "))
	}}
	root.AddCommand(runCmd)
	ok = true // справка — без прогона
	p.Workflows.AddTo(runCmd, func(_ *cobra.Command, command *cliche.Command, in cliche.Input) error {
		ok, err = start(ctx, o, p, cwd, command, in, stdout)
		return nil
	})
	root.SetArgs(append([]string{"run"}, o.Args...))
	root.SetOut(stdout)
	root.SetErr(os.Stderr)
	if runErr := root.ExecuteContext(ctx); runErr != nil {
		return false, &runenv.InputError{Message: runErr.Error() + " — справка: llamer run <workflow> --help"}
	}
	return ok, err
}

// start — прогон workflow command с вызовом in. Временное прогона с копиями учётных данных удаляется при любом исходе.
func start(ctx context.Context, o *Options, p *Project, cwd string, command *cliche.Command, in cliche.Input,
	stdout io.Writer) (ok bool, err error) {
	vars, order := runenv.Environ()
	file, cfg, err := projectConfig(ctx, cwd, o.Config, vars)
	if err != nil {
		return false, err
	}
	contextDir := runenv.Resolve(cwd, o.Context)
	if info, err := os.Stat(contextDir); err != nil || !info.IsDir() {
		return false, &runenv.InputError{Message: "--context " + o.Context + ": каталога нет"}
	}
	logx.Log("конфиг: " + cfg.Source + ", workflow: " + command.File())
	run, err := artifacts.Open(filepath.Join(filepath.Dir(file), artifacts.Dir, "runs"), logx.StartedAt)
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
	// Клиент брокера — под именем каждой команды верхнего уровня.
	for _, c := range p.Commands.Commands {
		if err := env.Wrapper(c.Path[0], broker.Subcommand, c.Path[0]); err != nil {
			return false, err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	driver := func(string) (drivers.Driver, error) { return stub{}, nil }
	if o.DryRun {
		logx.Log("--dry-run: этапы идут на заглушке агента, без модели")
	} else {
		setup := drivers.Setup{Env: env.Vars, EnvOrder: env.Order, StageBin: env.Bin, Tmp: env.Tmp}
		if names := p.Library.Names(); len(names) > 0 {
			if setup.RunSkills, err = p.Library.Materialize(filepath.Join(run.Path, "stage-plugin"), names); err != nil {
				return false, err
			}
		}
		driver = prepared(ctx, cfg.Drivers, setup)
	}
	result, err := (&Run{Context: ctx, Project: p, Driver: driver, Artifacts: run, Cwd: contextDir, Out: stdout,
		Bin: env.Bin, ClientDir: filepath.Dir(exe)}).Execute(command, in)
	if err != nil {
		return false, err
	}
	return result.OK, nil
}

// prepared — драйвер по имени: окружение проверяется и драйвер создаётся к первому этапу на нём, так что прогон не
// требует драйверов, которых его этапы не зовут.
func prepared(ctx context.Context, settings map[string]drivers.Settings, setup drivers.Setup) func(string) (drivers.Driver, error) {
	ready := map[string]drivers.Driver{}
	var mu sync.Mutex
	return func(name string) (drivers.Driver, error) {
		mu.Lock()
		defer mu.Unlock()
		if d, ok := ready[name]; ok {
			return d, nil
		}
		gaps := drivers.Gaps(ctx, []string{name}, settings, setup.Env)
		// Пробел от проверки, сорванной сигналом, — не пробел окружения.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(gaps) > 0 {
			return nil, runenv.NotReady(gaps)
		}
		made, err := drivers.New([]string{name}, settings, setup)
		if err != nil {
			return nil, err
		}
		ready[name] = made[name]
		return made[name], nil
	}
}
