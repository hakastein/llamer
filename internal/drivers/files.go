package drivers

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hakastein/llamer/internal/redact"
)

// LinkSkills — каталог dir со ссылками на скиллы этапа из каталога скиллов прогона — и ни на какие больше.
func LinkSkills(dir, runSkills string, names []string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Symlink(filepath.Join(runSkills, "skills", name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// Exists — путь есть.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// UpToRepoRoot — существующие каталоги rel в cwd и выше до корня репозитория: там драйверы сами ищут скиллы дерева.
func UpToRepoRoot(cwd, rel string) []string {
	var found []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if Exists(filepath.Join(dir, rel)) {
			found = append(found, filepath.Join(dir, rel))
		}
		if Exists(filepath.Join(dir, ".git")) || filepath.Dir(dir) == dir {
			return found
		}
	}
}

// KeepRedacted — копия файла или каталога с вымаранными секретами: журнал
// сессии этапа уходит в артефакты, а в нём вывод команд агента.
func KeepRedacted(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := KeepRedacted(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
		return err
	}
	// Битые байты — U+FFFD: вымарывание ищет секреты в тексте.
	return os.WriteFile(to, []byte(redact.Secrets(strings.ToValidUTF8(string(data), "\uFFFD"))), 0o666)
}

// CopyFile — копия с правами исходника: копия кредов читается только владельцем, как оригинал.
func CopyFile(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.WriteFile(to, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(to, info.Mode().Perm())
}
