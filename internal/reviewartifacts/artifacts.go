package reviewartifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/smith/redact"
)

type Run struct {
	*artifacts.Run
	journalDir, briefsDir string
}

func Open(root string, startedAt time.Time) (*Run, error) {
	base, err := artifacts.Open(root, startedAt)
	if err != nil {
		return nil, err
	}
	r := &Run{Run: base, journalDir: filepath.Join(base.Path, "journal"), briefsDir: filepath.Join(base.Path, "briefs")}
	for _, dir := range []string{r.journalDir, r.briefsDir} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Run) OpenJournal(name string) *domain.Journal { return domain.OpenJournal(r.journalDir, name) }
func (r *Run) SaveBrief(stageKey string, brief *harness.Brief) (string, error) {
	data, err := json.MarshalIndent(brief, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(r.briefsDir, stageKey+".json")
	return path, os.WriteFile(path, append(data, '\n'), 0o666)
}

// RedactJournals — журналы пишет харнесс в окружении этапа, где секретов
// llamer нет, — и вымарать их там нечем. Вымарываются после прогона; сбой —
// ошибка прогона: невымаранный журнал иначе молча остался бы в артефактах.
func (r *Run) RedactJournals() error {
	entries, err := os.ReadDir(r.journalDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(r.journalDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if redacted := redact.Secrets(string(data)); redacted != string(data) {
			if err := os.WriteFile(path, []byte(redacted), 0o666); err != nil {
				return err
			}
		}
	}
	return nil
}
