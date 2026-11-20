// llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.
package main

import (
	"os"

	"github.com/hakastein/llamer/internal/cli"
	_ "github.com/hakastein/llamer/internal/drivers/claude"
	_ "github.com/hakastein/llamer/internal/drivers/codex"
)

func main() { os.Exit(cli.Execute(os.Args[1:], os.Stdout)) }
