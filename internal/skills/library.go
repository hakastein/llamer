package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const skillFile = "SKILL.md"

// Library — скиллы этапов: встроенные и из каталогов skillPaths, скилл —
// подкаталог с SKILL.md. Одно имя в двух каталогах skillPaths — ошибка: какой
// метод получит этап, решал бы порядок каталогов, а не тот, кто писал конфиг.
// Встроенный скилл одноимённым из skillPaths не подменяется и ошибкой не считается.
type Library struct {
	builtin fs.FS
	index   map[string]string // имя → каталог; встроенный — пустая строка
}

func LoadLibrary(builtinSkills fs.FS, paths []string) (*Library, error) {
	l := &Library{builtin: builtinSkills, index: map[string]string{}}
	builtin, _ := fs.ReadDir(builtinSkills, "skills")
	for _, e := range builtin {
		if _, err := fs.Stat(builtinSkills, path.Join("skills", e.Name(), skillFile)); e.IsDir() && err == nil {
			l.index[e.Name()] = ""
		}
	}
	for _, root := range paths {
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("каталога скиллов %s нет", root)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			if !(e.IsDir() || e.Type()&os.ModeSymlink != 0) {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, skillFile)); err != nil {
				continue
			}
			if prev, ok := l.index[e.Name()]; ok {
				if prev == "" {
					continue
				}
				return nil, fmt.Errorf("скилл %s есть в двух каталогах: %s и %s", e.Name(), prev, dir)
			}
			l.index[e.Name()] = dir
		}
	}
	return l, nil
}

func (l *Library) Missing(names []string) []string {
	var out []string
	for _, n := range names {
		if _, ok := l.index[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// AssertPresent — пропажу скилла ловим на сборке промпта: у агента она стоила
// бы хода и осталась бы в логе видимостью работы.
func (l *Library) AssertPresent(names []string) error {
	if missing := l.Missing(names); len(missing) > 0 {
		return fmt.Errorf("скиллы не найдены: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Materialize — скиллы прогона в `<dir>/skills/<имя>`: копия, а не ссылка —
// каталог ложится в артефакты и показывает текст метода, с которым шли этапы.
func (l *Library) Materialize(dir string, names []string) (string, error) {
	if err := l.AssertPresent(names); err != nil {
		return "", err
	}
	for _, name := range names {
		to := filepath.Join(dir, "skills", name)
		var err error
		if from := l.index[name]; from == "" {
			err = l.copyEmbedded(path.Join("skills", name), to)
		} else {
			err = copyTree(from, to)
		}
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (l *Library) copyEmbedded(from, to string) error {
	return fs.WalkDir(l.builtin, from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(to, strings.TrimPrefix(p, from))
		if d.IsDir() {
			return os.MkdirAll(target, 0o777)
		}
		data, err := fs.ReadFile(l.builtin, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o666)
	})
}

// copyTree — рекурсивная копия со ссылками, разыменованными в содержимое.
func copyTree(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(to, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, info.Mode().Perm())
}
