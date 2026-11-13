// llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.
package main

import (
	"os"

	"github.com/hakastein/llamer/internal/cli"
)

func main() { os.Exit(cli.Execute(os.Args[1:], os.Stdout)) }
