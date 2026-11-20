// Package drivers — общий интерфейс драйверов агента и их реестр. Драйвер
// запускает CLI агента в песочнице и отдаёт исход запуска в общей форме agent.
// Сами драйверы — в подпакетах, каждый регистрируется в init.
package drivers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/hakastein/llamer/internal/agent"
)

// Driver запускает CLI агента. События потока идут в телеметрию t, исход
// возвращается готовым; ошибка — запуск не подготовить, прогон дальше не идёт.
type Driver interface {
	Run(ctx context.Context, a Args, t *agent.Telemetry) (*agent.Outcome, error)
}

// Factory — то, что реестр знает о драйвере: настройки по умолчанию, проверка
// окружения и конструктор.
type Factory struct {
	Defaults Settings
	Gaps     func(ctx context.Context, s Settings, env map[string]string) []Gap
	New      func(s Settings, setup Setup) (Driver, error)
}

var registry = map[string]Factory{}

// Register добавляет драйвер под именем, которым его называет конфиг.
func Register(name string, f Factory) {
	if _, ok := registry[name]; ok {
		panic("драйвер " + name + " зарегистрирован дважды")
	}
	registry[name] = f
}

// Names — имена драйверов по алфавиту.
func Names() []string { return slices.Sorted(maps.Keys(registry)) }

// Get — драйвер по имени; имя уже проверено схемой конфига.
func Get(name string) Factory {
	f, ok := registry[name]
	if !ok {
		panic("драйвер " + name + " не зарегистрирован")
	}
	return f
}

// Gaps — чего не хватает окружению для драйверов names.
func Gaps(ctx context.Context, names []string, settings map[string]Settings, env map[string]string) []Gap {
	var gaps []Gap
	for _, name := range names {
		gaps = append(gaps, Get(name).Gaps(ctx, settings[name], env)...)
	}
	return gaps
}

// New создаёт драйверы names, каждый со своими настройками.
func New(names []string, settings map[string]Settings, setup Setup) (map[string]Driver, error) {
	out := map[string]Driver{}
	for _, name := range names {
		d, err := Get(name).New(settings[name], setup)
		if err != nil {
			return nil, err
		}
		out[name] = d
	}
	return out, nil
}

// Gap — чего не хватает окружению для драйвера: бинарника или входа.
type Gap struct {
	ID      string `yaml:"id"`
	Message string `yaml:"message"`
}

// Setup — окружение прогона, общее для всех запусков драйвера.
type Setup struct {
	Env       map[string]string
	EnvOrder  []string // порядок переменных Env: в нём они идут в окружение запуска
	StageBin  string   // каталог команд, которые агенту можно вызывать: встаёт первым в PATH
	RunSkills string   // каталог скиллов прогона или пусто
	Tmp       string   // временный каталог прогона: в нём драйвер готовит каталоги запусков
}

// Args — один запуск агента.
type Args struct {
	// Name — имя запуска, уникальное в прогоне: так называются его каталоги во временном каталоге.
	Name   string
	Model  string
	Effort string
	// ProjectInstructions — агент читает инструкции проекта (AGENTS.md, CLAUDE.md).
	ProjectInstructions bool
	Allow               []string // команды, которые агенту можно запускать
	// SystemPromptFile — файл, текст которого дописывается к системному промпту;
	// пусто — не дописывается. У запусков с одним файлом общий префикс кэша промпта.
	SystemPromptFile string
	Prompt           string
	Env              []string // переменные сверх окружения драйвера
	Skills           []string // скиллы запуска из каталога скиллов прогона
	Cwd              string
	// Schema — файл JSON Schema итогового ответа; пусто — ответ свободным текстом.
	Schema string
	// Sandbox — строгая песочница; nil — режим CLI только для чтения, где команды Allow разрешены.
	Sandbox *Sandbox
	Timeout time.Duration
	// SessionDir — куда сохранить журнал сессии для её продолжения; пусто — не сохранять.
	SessionDir string
}

// Sandbox — строгая песочница: агент читает только рабочий каталог, скиллы
// запуска и Readable, сети нет, unix-сокеты открыты.
type Sandbox struct{ Readable []string }

// Paths — что открыто на чтение: рабочий каталог первым, Readable и скиллы запуска.
func (s *Sandbox) Paths(a Args, runSkills string) []string {
	paths := append([]string{a.Cwd}, s.Readable...)
	for _, name := range a.Skills {
		paths = append(paths, filepath.Join(runSkills, "skills", name))
	}
	return paths
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
