// Package cli — команда llamer: разбор аргументов, вызов прогона ревью, скрытая подкоманда этапа.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/review"
	"github.com/hakastein/llamer/internal/runenv"
	"github.com/hakastein/smith/logx"
	"github.com/spf13/cobra"
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

func usage(message string) error { return &runenv.InputError{Message: message} }

// check — сочетания флагов; set — задан ли флаг.
func check(o *review.Options, set func(string) bool) error {
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

// runMain — прогон body с отменой по SIGINT и SIGTERM; возвращает код выхода.
func runMain(body func(ctx context.Context) (bool, error)) (code int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	var interrupted atomic.Int32
	go func() {
		select {
		case sig := <-signals:
			interrupted.Store(int32(sig.(syscall.Signal)))
			logx.Logf("прогон прерван: %s — этапы остановлены", sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			code = fail(fmt.Sprint(p))
		}
	}()
	ok, err := body(ctx)
	if sig := interrupted.Load(); sig != 0 {
		return 128 + int(sig)
	}
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

// Execute — команда llamer со скрытой подкомандой харнесса: этап зовёт ровно
// ту версию, что ведёт прогон. Возвращает код выхода.
func Execute(argv []string, stdout io.Writer) int {
	code := 0
	o := &review.Options{}
	root := &cobra.Command{Use: "llamer", Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := check(o, cmd.Flags().Changed); err != nil {
				return err
			}
			code = runMain(func(ctx context.Context) (bool, error) { return review.Main(ctx, o, stdout) })
			return nil
		}}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &runenv.InputError{Message: harness.FlagError(err) + " — справка: llamer --help"}
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
	root.AddCommand(&cobra.Command{Use: harness.Subcommand, Hidden: true, DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) { code = harness.Main(args, os.Stdout, os.Stderr) }})
	root.SetArgs(argv)
	root.SetOut(os.Stderr)
	root.SetErr(os.Stderr)
	if err := root.Execute(); err != nil {
		var input *runenv.InputError
		if !errors.As(err, &input) {
			err = &runenv.InputError{Message: err.Error() + " — справка: llamer --help"}
		}
		return fail(err.Error())
	}
	return code
}
