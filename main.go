// llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.
package main

import (
	"os"

	"github.com/hakastein/llamer/internal/cli"
	_ "github.com/hakastein/smith/drivers/claude"
	_ "github.com/hakastein/smith/drivers/codex"
	"github.com/hakastein/smith/logx"
)

func main() {
	logx.SetPrefix("[llamer] ")
	os.Exit(cli.Execute(os.Args[1:], os.Stdout))
}
