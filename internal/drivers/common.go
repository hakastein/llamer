// Package drivers — драйверы этапов: claude и codex. Драйвер готовит
// песочницу этапа и собирает его запуск; разбор потока приводит события к форме claude.
package drivers

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/redact"
)

// Окружение этапа — белый список: токен GitLab, переменные CI и всё прочее,
// чего здесь нет, в процесс модели не попадает. Нужное сверх этого драйверу
// задаёт `drivers.<имя>.env` конфига.
var baseEnv = []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "USER", "SHELL", "TERM",
	// сеть и доверие: без них драйвер за прокси не достучится до своего API
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// stageEnvOf — базовые переменные, вход драйвера (auth) и каталог команд этапа первым в PATH.
func stageEnvOf(env map[string]string, order []string, stageBin string, auth []string) []string {
	var out []string
	for _, name := range order {
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") || slices.Contains(auth, name) {
			if name != "PATH" {
				out = append(out, name+"="+env[name])
			}
		}
	}
	return append(out, "PATH="+stageBin+":"+env["PATH"])
}

// linkSkills — каталог dir со ссылками на скиллы этапа из каталога скиллов прогона — и ни на какие больше.
func linkSkills(dir, runSkills string, names []string) error {
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// upToRepoRoot — существующие каталоги rel в cwd и выше до корня репозитория: там драйверы сами ищут скиллы дерева.
func upToRepoRoot(cwd, rel string) []string {
	var found []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if exists(filepath.Join(dir, rel)) {
			found = append(found, filepath.Join(dir, rel))
		}
		if exists(filepath.Join(dir, ".git")) || filepath.Dir(dir) == dir {
			return found
		}
	}
}

// keepRedacted — копия файла или каталога с вымаранными секретами: журнал
// сессии этапа уходит в артефакты, а в нём вывод команд агента.
func keepRedacted(from, to string) error {
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
			if err := keepRedacted(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
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

// Gap — пробел окружения, найденный до прогона.
type Gap struct {
	ID      string `yaml:"id"`
	Message string `yaml:"message"`
}

func binaryGap(name string, settings config.Driver) []Gap {
	err := exec.Command(settings.Bin, "--version").Run()
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

// Shared — общая часть пакета: текст и файл с ним.
type Shared struct{ Text, Path string }

// LaunchArgs — что этап даёт драйверу для запуска.
type LaunchArgs struct {
	Stage         string
	Level         config.Level
	ProjectMemory bool
	Allow         []string
	Shared        *Shared
	Prompt        string
	Env           []string
	Skills        []string
	Cwd           string
}

// Driver — драйвер этапа.
type Driver interface {
	Name() string
	// SkillsHow — как этап берёт скиллы своего метода; SkillRef — как назван скилл в задании.
	SkillsHow() string
	SkillRef(name string) string
	// Quote — как пишется одинарная кавычка внутри текста в вызове.
	Quote() string
	Launch(a LaunchArgs) (*agent.Launch, error)
}

// Setup — то, что прогон даёт драйверу при подготовке.
type Setup struct {
	Settings  config.Driver
	Env       map[string]string
	EnvOrder  []string
	StageBin  string
	RunSkills string // каталог скиллов прогона или пусто
	Tmp       string
}

// Prepare — драйвер по имени.
func Prepare(name string, s Setup) (Driver, error) {
	if name == "codex" {
		return prepareCodex(s)
	}
	return prepareClaude(s)
}

// Gaps — пробелы драйвера: бинарник и вход.
func Gaps(name string, settings config.Driver, env map[string]string) []Gap {
	if name == "codex" {
		return codexGaps(settings, env)
	}
	return claudeGaps(settings, env)
}

func settingsEnv(s config.Driver) []string {
	var out []string
	for _, kv := range s.Env {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

func homeDir(env map[string]string) string {
	if h := env["HOME"]; h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// copyFile — копия с правами исходника: копия кредов читается только владельцем, как оригинал.
func copyFile(from, to string) error {
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
