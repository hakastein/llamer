// Package drivers — контракт драйвера этапов и их реестр. Драйвер проверяет
// своё окружение, готовит песочницу этапа, запускает его и отдаёт готовый исход
// в общей форме agent. Сами драйверы — в подпакетах, каждый регистрируется в init.
package drivers

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/hakastein/llamer/internal/agent"
)

// Driver — драйвер этапов до прогона: умолчания, проверка окружения, оформление
// задания под драйвер и подготовка.
type Driver interface {
	// Defaults — настройки драйвера, пока конфиг их не перекрыл.
	Defaults() Settings
	// Gaps — пробелы окружения драйвера: бинарник и вход.
	Gaps(ctx context.Context, settings Settings, env map[string]string) []Gap
	// SkillsHow — как этап берёт скиллы своего метода; SkillRef — как назван скилл в задании.
	SkillsHow() string
	SkillRef(name string) string
	// Quote — как пишется одинарная кавычка внутри текста в вызове.
	Quote() string
	// Prepare — драйвер, готовый к этапам прогона.
	Prepare(s Setup) (Runner, error)
}

// Runner — драйвер, готовый к прогону: запускает этап. События потока идут в
// телеметрию t, исход возвращается готовым. Ошибка — этап не запустить: прогон не продолжить.
type Runner interface {
	Run(ctx context.Context, a LaunchArgs, t *agent.Telemetry) (*agent.Outcome, error)
}

var registry = map[string]Driver{}

// Register — драйвер под именем, которым его называет конфиг.
func Register(name string, d Driver) {
	if _, ok := registry[name]; ok {
		panic("драйвер " + name + " зарегистрирован дважды")
	}
	registry[name] = d
}

// Names — имена драйверов по алфавиту.
func Names() []string { return slices.Sorted(maps.Keys(registry)) }

// Get — драйвер по имени; имя уже проверено схемой конфига.
func Get(name string) Driver {
	d, ok := registry[name]
	if !ok {
		panic("драйвер " + name + " не зарегистрирован")
	}
	return d
}

// Gaps — пробелы окружения драйверов names.
func Gaps(ctx context.Context, names []string, settings map[string]Settings, env map[string]string) []Gap {
	var gaps []Gap
	for _, name := range names {
		gaps = append(gaps, Get(name).Gaps(ctx, settings[name], env)...)
	}
	return gaps
}

// Prepare — драйверы names, готовые к прогону: каждый получает setup со своими настройками.
func Prepare(names []string, settings map[string]Settings, setup Setup) (map[string]Runner, error) {
	prepared := map[string]Runner{}
	for _, name := range names {
		setup.Settings = settings[name]
		l, err := Get(name).Prepare(setup)
		if err != nil {
			return nil, err
		}
		prepared[name] = l
	}
	return prepared, nil
}

// Gap — пробел окружения, найденный до прогона.
type Gap struct {
	ID      string `yaml:"id"`
	Message string `yaml:"message"`
}

// Setup — то, что прогон даёт драйверу при подготовке.
type Setup struct {
	Settings  Settings
	Env       map[string]string
	EnvOrder  []string
	StageBin  string
	RunSkills string // каталог скиллов прогона или пусто
	Tmp       string
}

// Shared — общая часть пакета: текст и файл с ним.
type Shared struct{ Text, Path string }

// LaunchArgs — что этап даёт драйверу для запуска.
type LaunchArgs struct {
	Stage         string
	Level         Level
	ProjectMemory bool
	Allow         []string
	Shared        *Shared
	Prompt        string
	Env           []string
	Skills        []string
	Cwd           string
	// Schema — файл JSON Schema итогового ответа; пусто — ответ свободным текстом.
	Schema string
	// Sandbox — строгая песочница этапа; nil — песочница этапа ревью.
	Sandbox *Sandbox
	Timeout time.Duration
	// SessionDir — куда сохранить журнал сессии этапа для её продолжения; пусто — не сохранять.
	SessionDir string
}

// Sandbox — строгая песочница этапа `llamer run`: агент читает только каталог
// контекста, скиллы этапа и Readable, сети нет, unix-сокеты открыты.
type Sandbox struct{ Readable []string }

// Paths — что этапу открыто на чтение: каталог контекста первым, Readable и скиллы этапа.
func (s *Sandbox) Paths(a LaunchArgs, runSkills string) []string {
	paths := append([]string{a.Cwd}, s.Readable...)
	for _, name := range a.Skills {
		paths = append(paths, filepath.Join(runSkills, "skills", name))
	}
	return paths
}
