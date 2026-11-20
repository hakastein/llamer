package drivers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/redact"
)

// Окружение этапа — белый список: токен GitLab, переменные CI и всё прочее,
// чего здесь нет, в процесс модели не попадает. Нужное сверх этого драйверу
// задаёт `drivers.<имя>.env` конфига.
var baseEnv = []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "USER", "SHELL", "TERM",
	// сеть и доверие: без них драйвер за прокси не достучится до своего API
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// StageEnv — окружение этапа: базовые переменные, вход драйвера auth, каталог
// команд этапа первым в PATH и переменные из настроек драйвера.
func (s Setup) StageEnv(auth []string) []string {
	var out []string
	for _, name := range s.EnvOrder {
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") || slices.Contains(auth, name) {
			if name != "PATH" {
				out = append(out, name+"="+s.Env[name])
			}
		}
	}
	out = append(out, "PATH="+s.StageBin+":"+s.Env["PATH"])
	for _, kv := range s.Settings.Env {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// LinkSkills — каталог dir со ссылками на скиллы этапа из каталога скиллов прогона — и ни на какие больше.
func LinkSkills(dir, runSkills string, names []string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Symlink(filepath.Join(runSkills, "skills", name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// Exists — путь есть.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// UpToRepoRoot — существующие каталоги rel в cwd и выше до корня репозитория: там драйверы сами ищут скиллы дерева.
func UpToRepoRoot(cwd, rel string) []string {
	var found []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if Exists(filepath.Join(dir, rel)) {
			found = append(found, filepath.Join(dir, rel))
		}
		if Exists(filepath.Join(dir, ".git")) || filepath.Dir(dir) == dir {
			return found
		}
	}
}

// KeepRedacted — копия файла или каталога с вымаранными секретами: журнал
// сессии этапа уходит в артефакты, а в нём вывод команд агента.
func KeepRedacted(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := KeepRedacted(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
		return err
	}
	// Битые байты — U+FFFD: вымарывание ищет секреты в тексте.
	return os.WriteFile(to, []byte(redact.Secrets(strings.ToValidUTF8(string(data), "\uFFFD"))), 0o666)
}

// BinaryGap — пробел, если бинарник драйвера name не отвечает на --version.
func BinaryGap(ctx context.Context, name string, settings Settings) []Gap {
	cmd := exec.CommandContext(ctx, settings.Bin, "--version")
	agent.Group(cmd)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	reason := ""
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		reason = fmt.Sprintf("код %d", exit.ExitCode())
	default:
		reason = err.Error()
	}
	return []Gap{{"driver-" + name, fmt.Sprintf("драйвер %s: %s не запускается (%s)", name, settings.Bin, reason)}}
}

// HomeDir — HOME окружения прогона.
func HomeDir(env map[string]string) string {
	if h := env["HOME"]; h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// CopyFile — копия с правами исходника: копия кредов читается только владельцем, как оригинал.
func CopyFile(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(to, info.Mode().Perm())
}
