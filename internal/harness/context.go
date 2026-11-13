package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/materials"
)

// Brief — задание этапа: JSON-файл, путь до него приходит переменной окружения.
// Siblings — экземпляры того же этапа на других драйверах: их находок этап не
// видит. RelatedRequired — требовать ли у находки `--related`.
type Brief struct {
	Stage           string             `json:"stage"`
	Driver          string             `json:"driver,omitempty"`
	Name            string             `json:"name"`
	Siblings        []string           `json:"siblings"`
	JournalDir      string             `json:"journalDir"`
	BaseSha         string             `json:"baseSha"`
	DiffPath        string             `json:"diffPath"`
	Revs            []string           `json:"revs"`
	RelatedRequired bool               `json:"relatedRequired"`
	Commands        []string           `json:"commands"`
	Threads         []materials.Thread `json:"threads,omitempty"`
}

func readBrief(path string) (*Brief, error) {
	if path == "" {
		return nil, errors.New(BriefEnv + " не задан: команда запущена вне этапа ревью")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Этапу комментариев место докажет сам комментарий: требование снимается только явно.
	b := &Brief{RelatedRequired: true}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, err
	}
	for _, field := range [][2]string{{"stage", b.Stage}, {"journalDir", b.JournalDir}, {"diffPath", b.DiffPath}} {
		if field[1] == "" {
			return nil, errors.New("задание этапа неполно: нет поля " + field[0])
		}
	}
	return b, nil
}

// Context — зависимости команд; берутся по первому обращению, чтобы `stop` работал и без диффа.
type Context struct {
	brief   *Brief
	journal *domain.Journal
	cwd     string
	state   *domain.State
	diff    *materials.Diff
}

func (c *Context) Stage() string { return c.brief.Stage }

func (c *Context) Commands() []string { return c.brief.Commands }

func (c *Context) State() *domain.State {
	if c.state == nil {
		c.state = domain.ReadRunState(c.brief.JournalDir).HideStages(c.brief.Siblings)
	}
	return c.state
}

func (c *Context) Diff() (*materials.Diff, error) {
	if c.diff == nil {
		data, err := os.ReadFile(c.brief.DiffPath)
		if err != nil {
			return nil, err
		}
		if c.diff, err = materials.FromText(string(data), c.brief.BaseSha, nil); err != nil {
			return nil, err
		}
	}
	return c.diff, nil
}

func (c *Context) ReadFileLines(file string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(c.cwd, file))
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}
