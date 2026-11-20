// Package artifacts — промпты, потоки событий, сессии и расход прогона.
package artifacts

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/redact"
)

const Dir = ".llamer"

var slugJunk = regexp.MustCompile(`[^\wА-яЁё]+`)

func StageSlug(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(slugJunk.ReplaceAllString(name, "-"), "-"), "-")
}

// Run — каталог артефактов прогона.
type Run struct {
	Path string
}

// Open — каталог прогона `<дата>_<время>`, занятое имя — с суффиксом `-2`,
// `-3`…: каталог создаётся атомарно, и два прогона в одну секунду не делят
// журналы. `.gitignore` со звёздочкой внутри каталога артефактов: снимок
// рабочего дерева (`--head worktree`) и поиск по коду иначе захватили бы
// артефакты прошлых прогонов.
func Open(root string, startedAt time.Time) (*Run, error) {
	stamp := startedAt.UTC().Format("2006-01-02_15-04-05")
	base := filepath.Join(root, Dir)
	if err := os.MkdirAll(base, 0o777); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(base, ".gitignore"), []byte("*\n"), 0o666); err != nil {
		return nil, err
	}
	path := filepath.Join(base, stamp)
	for n := 2; ; n++ {
		err := os.Mkdir(path, 0o777)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		path = filepath.Join(base, fmt.Sprintf("%s-%d", stamp, n))
	}
	r := &Run{Path: path}
	return r, nil
}

type stream struct {
	mu     sync.Mutex
	file   *os.File
	failed bool
	name   string
}

func (s *stream) fail(err error) {
	if !s.failed {
		s.failed = true
		logx.Logf("[%s] сырой поток этапа не пишется (%s) — в артефакты не ляжет", s.name, err)
	}
}

func (s *stream) Write(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return
	}
	if _, err := s.file.WriteString(redact.Secrets(string(line)) + "\n"); err != nil {
		s.fail(err)
	}
}

func (s *stream) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

// OpenStream — сырой поток этапа. Сбой записи потока прогон не роняет:
// разбор и публикация от него не зависят.
func (r *Run) OpenStream(stageName string) agent.Stream {
	s := &stream{name: stageName}
	f, err := os.Create(filepath.Join(r.Path, StageSlug(stageName)+".jsonl"))
	if err != nil {
		s.fail(err)
		return s
	}
	s.file = f
	return s
}

func (r *Run) SavePrompt(stageName, text string) string {
	path := filepath.Join(r.Path, StageSlug(stageName)+".prompt.md")
	if err := os.WriteFile(path, []byte(redact.Secrets(text)), 0o666); err != nil {
		logx.Logf("[%s] задание не сохранено (%s)", stageName, err)
	}
	return path
}

// SessionDir — журнал сессии этапа: им разбирают ход этапа и продолжают её вопросом к агенту.
func (r *Run) SessionDir(stageName string) string {
	return filepath.Join(r.Path, "sessions", StageSlug(stageName))
}

func (r *Run) SaveDiff(name, text string) (string, error) {
	path := filepath.Join(r.Path, name+".diff")
	return path, os.WriteFile(path, []byte(text), 0o666)
}

func (r *Run) SaveText(name, text string) (string, error) {
	path := filepath.Join(r.Path, name)
	return path, os.WriteFile(path, []byte(redact.Secrets(text)), 0o666)
}

func (r *Run) ReportCost(records []*agent.Record, elapsedMs int64) {
	if len(records) == 0 {
		return
	}
	for _, line := range strings.Split(agent.FormatCostTable(records, elapsedMs), "\n") {
		logx.Log(line)
	}
	limits := agent.DriverLimits(records)
	for _, driver := range slices.Sorted(maps.Keys(limits)) {
		logx.Log(agent.FormatLimits(driver, limits[driver]))
	}
	path := filepath.Join(r.Path, "cost.json")
	if err := agent.WriteCostJSON(path, records); err != nil {
		panic(err)
	}
	logx.Log("стоимость по этапам: " + path)
	for _, rec := range records {
		if rec.Resume != "" {
			logx.Logf("продолжить сессию «%s» вопросом: %s '<вопрос>'", rec.Name, rec.Resume)
		}
	}
}
