// Package git — вызовы git прогона.
package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Error — git завершился с ошибкой.
type Error struct {
	Args           []string
	Status         int // -1 — git не запустился
	Stdout, Stderr string
	Cause          error
}

func (e *Error) Error() string {
	if e.Status < 0 {
		return "git не запустился: " + e.Cause.Error()
	}
	return "git " + strings.Join(e.Args, " ") + ": код " + strconv.Itoa(e.Status) + "\n" + e.Stderr
}

// FirstLine — первая строка stderr ошибки, а без него — её текста.
func FirstLine(err error) string {
	var g *Error
	text := err.Error()
	if errors.As(err, &g) && g.Stderr != "" {
		text = g.Stderr
	}
	return strings.Split(strings.TrimSpace(text), "\n")[0]
}

// Raw — git как есть: вывод без обрезки.
func Raw(args []string, cwd string, env ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	gitErr := &Error{Args: args, Status: -1, Stdout: stdout.String(), Stderr: stderr.String(), Cause: err}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		gitErr.Status = exit.ExitCode()
	}
	return "", gitErr
}

// Git — вывод git без концевых переводов строк; пробелы значимы (пустая строка контекста диффа — « »). env — дополнительные NAME=value.
func Git(args []string, cwd string, env ...string) (string, error) {
	out, err := Raw(args, cwd, env...)
	return strings.TrimRight(out, "\n"), err
}

// Try — вывод git или ok=false при любой ошибке.
func Try(args []string, cwd string) (string, bool) {
	out, err := Git(args, cwd)
	return out, err == nil
}

// Short — короткий sha: первые восемь знаков.
func Short(sha string) string { return sha[:min(8, len(sha))] }

func RepoRoot(cwd string) (string, error) {
	return Git([]string{"rev-parse", "--show-toplevel"}, cwd)
}

func IsAncestor(sha, descendant, cwd string) bool {
	_, ok := Try([]string{"merge-base", "--is-ancestor", sha, descendant}, cwd)
	return ok
}

func CurrentHead(cwd string) (string, error) {
	return Git([]string{"rev-parse", "HEAD"}, cwd)
}

// NoCommonHistory — нет общего предка: в неглубоком клоне — потому что истории не хватает, её догружает вызывающий.
func NoCommonHistory(head, rev, cwd string) string {
	shallow, _ := Try([]string{"rev-parse", "--is-shallow-repository"}, cwd)
	text := "у " + Short(head) + " и " + rev + " нет общей истории"
	if shallow == "true" {
		text += " — клон неглубокий: нужна полная по коммитам история (git fetch --unshallow --filter=tree:0)"
	}
	return text
}
