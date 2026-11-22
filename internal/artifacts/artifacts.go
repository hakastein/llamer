// Package artifacts — каталог прогона: промпты, потоки событий, сессии и тексты.
package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/redact"
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

// lineFile — файл строк потока этапа. Сбой записи прогон не роняет: разбор и
// публикация от потока не зависят, — о нём один раз говорит лог.
type lineFile struct {
	file   *os.File
	failed bool
	stage  string
	what   string
}

func openLines(path, stage, what string) *lineFile {
	f := &lineFile{stage: stage, what: what}
	file, err := os.Create(path)
	if err != nil {
		f.fail(err)
		return f
	}
	f.file = file
	return f
}

func (f *lineFile) fail(err error) {
	if !f.failed {
		f.failed = true
		logx.Logf("[%s] %s этапа не пишется (%s) — в артефакты не ляжет", f.stage, f.what, err)
	}
}

func (f *lineFile) write(line string) {
	if f.file == nil {
		return
	}
	if _, err := f.file.WriteString(redact.Secrets(line) + "\n"); err != nil {
		f.fail(err)
	}
}

func (f *lineFile) close() {
	if f.file != nil {
		f.file.Close()
		f.file = nil
	}
}

// stream — поток этапа: события llamer в `<этап>.jsonl`, вывод процесса как есть — в `<этап>.raw.jsonl`.
type stream struct {
	mu          sync.Mutex
	events, raw *lineFile
}

func (s *stream) Write(e *agent.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events.write(string(data))
}

func (s *stream) Raw(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.write(string(line))
}

func (s *stream) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events.close()
	s.raw.close()
}

// OpenStream — поток этапа в артефактах.
func (r *Run) OpenStream(stageName string) agent.Stream {
	base := filepath.Join(r.Path, StageSlug(stageName))
	return &stream{events: openLines(base+".jsonl", stageName, "поток"), raw: openLines(base+".raw.jsonl", stageName, "сырой вывод")}
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
