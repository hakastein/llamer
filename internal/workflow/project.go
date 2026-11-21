package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/runenv"
	"github.com/hakastein/llamer/internal/skills"
)

// DirName — каталог llamer проекта: workflow, команды агента, библиотеки, промпты, схемы, скиллы.
const DirName = ".llamer"

// DirEnv — переменная окружения с каталогом llamer проекта.
const DirEnv = "LLAMER_DIR"

// Project — каталог llamer проекта, прочитанный как один источник cliche: так workflow и команды агента делят
// библиотеки — require не выходит за источник.
type Project struct {
	Dir     string
	Library *skills.Library // скиллы из `skills/`
	// Workflows — скрипты `workflows/`, Commands — команды агента из `commands/`; путь — без первого слова.
	Workflows, Commands *cliche.Catalog
}

// FindDir — каталог llamer проекта: flag, иначе env, иначе ближайший `.llamer/` вверх от cwd.
func FindDir(cwd, flag, env string) (string, error) {
	if flag != "" {
		return runenv.Resolve(cwd, flag), nil
	}
	if env != "" {
		return runenv.Resolve(cwd, env), nil
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, DirName)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path, nil
		}
		if filepath.Dir(dir) == dir {
			return "", &runenv.InputError{Message: "каталога " + DirName + " нет ни в " + cwd + ", ни выше — задай --dir или " + DirEnv}
		}
	}
}

// LoadProject — каталог dir. Сломанная команда агента останавливает прогон до первого запроса к модели;
// сломанный workflow — только свой вызов.
func LoadProject(dir string) (*Project, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, &runenv.InputError{Message: "каталога llamer " + dir + " нет"}
	}
	roots := cliche.Config{Sources: []cliche.Source{cliche.Dir(dir)}}.Scan()
	if roots[0].Err != nil {
		return nil, roots[0].Err
	}
	all := cliche.Merge(roots)
	p := &Project{Dir: dir, Workflows: &cliche.Catalog{}, Commands: &cliche.Catalog{}}
	parts := map[string]*cliche.Catalog{"workflows": p.Workflows, "commands": p.Commands}
	for _, c := range all.Commands {
		if part := parts[c.Path[0]]; part != nil && len(c.Path) > 1 {
			c.Path = c.Path[1:]
			part.Commands = append(part.Commands, c)
		}
	}
	var broken []error
	for _, b := range all.Broken {
		switch {
		case len(b.Path) > 1 && b.Path[0] == "workflows":
			p.Workflows.Broken = append(p.Workflows.Broken, cliche.Broken{Path: b.Path[1:], Err: b.Err})
		case len(b.Path) > 0 && b.Path[0] == "commands":
			broken = append(broken, b.Err)
		}
	}
	if err := errors.Join(broken...); err != nil {
		return nil, &runenv.InputError{Message: fmt.Sprintf("команды агента в %s: %v", filepath.Join(dir, "commands"), err)}
	}
	var err error
	p.Library, err = skills.LoadLibrary(os.DirFS(dir), nil)
	return p, err
}
