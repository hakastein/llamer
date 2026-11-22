// Package runenv — окружение прогона: окружение процесса, проверка входа, временный каталог с каталогом команд
// этапа и драйверы этапов.
package runenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/hakastein/smith/drivers"
	"github.com/hakastein/smith/logx"
	"github.com/hakastein/smith/skills"
)

// InputError — вход прогона не годится для разбора: причину читает человек, а не стек.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

// Environ — окружение процесса: значения и порядок имён.
func Environ() (map[string]string, []string) {
	env := map[string]string{}
	var order []string
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if _, seen := env[name]; !seen {
			order = append(order, name)
		}
		env[name] = value
	}
	return env, order
}

// Resolve — путь из аргумента относительно cwd.
func Resolve(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// NotReady — отказ начать прогон из-за пробелов окружения.
func NotReady(gaps []drivers.Gap) error {
	lines := make([]string, len(gaps))
	for i, g := range gaps {
		lines[i] = "- " + g.Message
	}
	return &InputError{"прогон не начат — окружение не готово:\n" + strings.Join(lines, "\n")}
}

// Env — окружение прогона и его временный каталог: приватные конфиги драйверов
// с копией кредов, пакет, каталог команд этапа — после прогона не должно остаться ничего.
type Env struct {
	Vars  map[string]string
	Order []string
	Tmp   string
	// Bin — каталог команд этапа, первым в PATH этапа: пустой, но не пустая
	// строка — та значила бы текущий каталог.
	Bin string
}

// Open — временный каталог прогона с пустым каталогом команд этапа.
func Open(vars map[string]string, order []string) (*Env, error) {
	tmp, err := os.MkdirTemp("", "llamer-run-")
	if err != nil {
		return nil, err
	}
	e := &Env{Vars: vars, Order: order, Tmp: tmp, Bin: filepath.Join(tmp, "bin")}
	if err := os.MkdirAll(e.Bin, 0o777); err != nil {
		return nil, errors.Join(err, e.Close())
	}
	return e, nil
}

// Close — удаляет временный каталог; неудача — ошибка: в нём копии учётных данных.
func (e *Env) Close() error {
	if err := os.RemoveAll(e.Tmp); err != nil {
		err = fmt.Errorf("временный каталог с копиями учётных данных не удалён: %w", err)
		logx.Log(err.Error())
		return err
	}
	return nil
}

// Wrapper — обёртка name в каталоге команд этапа: бинарник, что ведёт прогон,
// с аргументами args. Пишется на каждый прогон: одноимённая команда в PATH
// этапа могла бы оказаться чужой.
func (e *Env) Wrapper(name string, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := "#!/bin/sh\nexec " + shellescape.Quote(exe)
	for _, arg := range args {
		script += " " + shellescape.Quote(arg)
	}
	script += " \"$@\"\n"
	path := filepath.Join(e.Bin, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// Drivers создаёт драйверы names со своими настройками; скиллы skillNames из
// library копируются в <runPath>/stage-plugin.
func (e *Env) Drivers(names []string, settings map[string]drivers.Settings, library *skills.Library, skillNames []string,
	runPath string) (map[string]drivers.Driver, error) {
	setup := drivers.Setup{Env: e.Vars, EnvOrder: e.Order, StageBin: e.Bin, Tmp: e.Tmp}
	if len(skillNames) > 0 {
		var err error
		if setup.RunSkills, err = library.Materialize(filepath.Join(runPath, "stage-plugin"), skillNames); err != nil {
			return nil, err
		}
	}
	return drivers.New(names, settings, setup)
}
