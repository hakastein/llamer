package cli

import (
	"context"
	"io"

	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/runenv"
	"github.com/hakastein/llamer/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const RunHelp = `llamer run [флаги llamer] <workflow> [аргументы workflow] — скрипт workflow из .llamer/workflows/.

Флаги llamer — до имени workflow:
  --dir <каталог>             каталог llamer проекта; по умолчанию LLAMER_DIR или ближайший .llamer/ вверх от
                              текущего каталога
  --context <каталог>         рабочий каталог агента с подготовленным контекстом; по умолчанию текущий
  --config <файл>             конфиг проекта вместо .llamer.json в корне репозитория
  --dry-run                   этапы без модели: заглушка агента, ответ — пустая строка или наименьшее значение
                              по схеме этапа; ошибки скрипта и спеков этапов — те же, что у прогона
  -h, --help                  эта справка

Аргументы workflow — из его объявления: llamer run <workflow> --help.

Выход: строки JSON в stdout — события этапов, последней — результат; результат ещё и в
.llamer/runs/<прогон>/result.json рядом с конфигом проекта.
`

// runCommand — llamer run. Флаги llamer идут до имени workflow: всё после него — вызов скрипта, его разбирает
// команда, которую cliche собирает из объявления workflow.
func runCommand(stdout io.Writer, code *int) *cobra.Command {
	return &cobra.Command{Use: "run", DisableFlagParsing: true, SilenceErrors: true, SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			o := &workflow.Options{}
			f := pflag.NewFlagSet("run", pflag.ContinueOnError)
			f.SetInterspersed(false)
			f.SetOutput(io.Discard)
			f.StringVar(&o.Dir, "dir", "", "")
			f.StringVar(&o.Context, "context", ".", "")
			f.StringVar(&o.Config, "config", "", "")
			f.BoolVar(&o.DryRun, "dry-run", false, "")
			help := f.BoolP("help", "h", false, "")
			if err := f.Parse(args); err != nil {
				return &runenv.InputError{Message: harness.FlagError(err) + " — справка: llamer run --help"}
			}
			if *help || f.NArg() == 0 {
				io.WriteString(stdout, RunHelp)
				return nil
			}
			o.Args = f.Args()
			*code = runMain(func(ctx context.Context) (bool, error) { return workflow.Main(ctx, o, stdout) })
			return nil
		}}
}
