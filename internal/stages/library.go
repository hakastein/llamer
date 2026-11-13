// Package stages — этапы прогона и их протокол: оценка, треды, разбирающие
// этапы, дедупликация, скептик.
package stages

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

//go:embed all:skills
var builtinSkills embed.FS

//go:embed templates/*.md
var templates embed.FS

const skillFile = "SKILL.md"

// Library — скиллы этапов: встроенные и из каталогов skillPaths, скилл —
// подкаталог с SKILL.md. Одно имя в двух каталогах skillPaths — ошибка: какой
// метод получит этап, решал бы порядок каталогов, а не тот, кто писал конфиг.
// Встроенный скилл одноимённым из skillPaths не подменяется и ошибкой не считается.
type Library struct {
	index map[string]string // имя → каталог; встроенный — пустая строка
}

func LoadLibrary(paths []string) (*Library, error) {
	l := &Library{index: map[string]string{}}
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
			err = copyEmbedded(path.Join("skills", name), to)
		} else {
			err = copyTree(from, to)
		}
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}

func copyEmbedded(from, to string) error {
	return fs.WalkDir(builtinSkills, from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(to, strings.TrimPrefix(p, from))
		if d.IsDir() {
			return os.MkdirAll(target, 0o777)
		}
		data, err := builtinSkills.ReadFile(p)
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

var placeholder = regexp.MustCompile(`\[([A-Z][A-Z0-9_]*)]`)

// template — задание этапа. Незаполненная дырка уехала бы к модели текстом
// `[PACK]`, поэтому оба перекоса караются на сборке.
type template struct {
	key, text string
	names     []string
}

func loadTemplate(key string) *template {
	data, err := templates.ReadFile("templates/" + key + ".md")
	if err != nil {
		panic(err)
	}
	t := &template{key: key, text: string(data)}
	for _, m := range placeholder.FindAllStringSubmatch(t.text, -1) {
		if !slices.Contains(t.names, m[1]) {
			t.names = append(t.names, m[1])
		}
	}
	return t
}

func (t *template) has(name string) bool { return slices.Contains(t.names, name) }

var blankRuns = regexp.MustCompile(`\n{3,}`)

func (t *template) render(values map[string]string, order []string) (string, error) {
	var missing, extra []string
	for _, n := range t.names {
		if _, ok := values[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("шаблон %s: нечем заполнить %s", t.key, strings.Join(missing, ", "))
	}
	for _, n := range order {
		if !t.has(n) {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		return "", fmt.Errorf("шаблон %s: некуда подставить %s", t.key, strings.Join(extra, ", "))
	}
	text := placeholder.ReplaceAllStringFunc(t.text, func(m string) string { return values[m[1:len(m)-1]] })
	return strings.TrimRightFunc(blankRuns.ReplaceAllString(text, "\n\n"), unicode.IsSpace) + "\n", nil
}
