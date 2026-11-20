package cli

import (
	"context"
	"io"

	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/runenv"
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

func runCommand(stdout io.Writer, code *int) *cobra.Command {
	o := &workflow.Options{}
	cmd := &cobra.Command{Use: "run", SilenceErrors: true, SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usage("нужен ровно один каталог workflow — справка: llamer run --help")
			}
			o.Workflow = args[0]
			*code = runMain(func(ctx context.Context) (bool, error) { return workflow.Main(ctx, o, stdout) })
			return nil
		}}
	cmd.SetHelpFunc(func(*cobra.Command, []string) { io.WriteString(stdout, RunHelp) })
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &runenv.InputError{Message: harness.FlagError(err) + " — справка: llamer run --help"}
	})
	f := cmd.Flags()
	f.StringVar(&o.Context, "context", ".", "")
	f.StringVar(&o.Config, "config", "", "")
	return cmd
}
