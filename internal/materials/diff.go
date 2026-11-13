// Package materials — то, что разбирают этапы: дифф изменений задачи, треды,
// пакет контекста.
package materials

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/hakastein/llamer/internal/git"
	gl "gitlab.com/gitlab-org/api/client-go"
)

// Line — строка одной стороны диффа: встречный номер и текст.
type Line struct {
	Other int
	Text  string
}

// Lines — строки файла в порядке диффа.
type Lines struct {
	Order []int
	ByNum map[int]Line
}

func (l *Lines) set(n int, line Line) {
	if _, ok := l.ByNum[n]; !ok {
		l.Order = append(l.Order, n)
	}
	l.ByNum[n] = line
}

func (l *Lines) Len() int {
	if l == nil {
		return 0
	}
	return len(l.Order)
}

// Side — файлы одной стороны диффа в порядке появления.
type Side struct {
	Files  []string
	ByFile map[string]*Lines
}

func newSide() *Side { return &Side{ByFile: map[string]*Lines{}} }

func (s *Side) Get(file string) *Lines { return s.ByFile[file] }

func (s *Side) of(file string) *Lines {
	if _, ok := s.ByFile[file]; !ok {
		s.Files = append(s.Files, file)
		s.ByFile[file] = &Lines{ByNum: map[int]Line{}}
	}
	return s.ByFile[file]
}

// ParseUnifiedDiff — строки обеих сторон диффа. Added — новые строки с
// номером старой стороны на их месте, Removed — наоборот: GitLab адресует
// строку парой «старый номер, новый номер», и без неё многострочный тред
// создаётся, но подсветка диапазона не встаёт.
func ParseUnifiedDiff(text string) (added, removed *Side, err error) {
	files, _, err := gitdiff.Parse(strings.NewReader(text))
	if err != nil {
		return nil, nil, fmt.Errorf("дифф не разобран: %w", err)
	}
	added, removed = newSide(), newSide()
	for _, f := range files {
		if f.IsBinary || len(f.TextFragments) == 0 {
			continue
		}
		var addedSide, removedSide *Lines
		if f.OldName != "" {
			removedSide = removed.of(f.OldName)
		}
		if f.NewName != "" {
			addedSide = added.of(f.NewName)
		}
		for _, frag := range f.TextFragments {
			oldPos, newPos := int(frag.OldPosition), int(frag.NewPosition)
			for _, l := range frag.Lines {
				text := strings.TrimSuffix(l.Line, "\n")
				switch l.Op {
				case gitdiff.OpDelete:
					if removedSide != nil {
						removedSide.set(oldPos, Line{Other: newPos, Text: text})
					}
					oldPos++
				case gitdiff.OpAdd:
					if addedSide != nil {
						addedSide.set(newPos, Line{Other: oldPos, Text: text})
					}
					newPos++
				default:
					oldPos++
					newPos++
				}
			}
		}
	}
	return added, removed, nil
}

// Diff — изменения по файлам.
type Diff struct {
	BaseRef string
	// Files включает удалённые файлы: сторона Added их не хранит, а список
	// служит ещё и pathspec'ом для широкого диффа — без них он по таким файлам пуст.
	Files          []string
	Added, Removed *Side
	Revs           []string
	cwd            string
	read           func(string) (string, bool)
	widened        map[int]string
	// Путь до переименования: GitLab не принимает позицию, где old_path равен
	// new_path у переименованного файла, и вешает тред на реквест целиком.
	renamedFrom map[string]string
	SourceText  *string
}

// OverRange — дифф между ревизиями репозитория.
func OverRange(baseRef string, revs []string, cwd string) (*Diff, error) {
	renamed := map[string]string{}
	out, err := git.Git(append([]string{"diff", "--name-status"}, revs...), cwd)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		p := ""
		if len(parts) > 1 {
			p = parts[1]
		}
		if len(parts) > 2 {
			renamed[parts[2]] = p
			p = parts[2]
		}
		files = append(files, p)
	}
	narrow, err := git.Git(append([]string{"diff", "-U0"}, revs...), cwd)
	if err != nil {
		return nil, err
	}
	added, removed, err := ParseUnifiedDiff(narrow)
	if err != nil {
		return nil, err
	}
	// Узкий дифф уже посчитан — кладём в кэш ширин, чтобы Text(0) не звал git второй раз.
	return &Diff{BaseRef: baseRef, Files: files, Added: added, Removed: removed, Revs: revs, cwd: cwd,
		widened: map[int]string{0: narrow}, renamedFrom: renamed}, nil
}

// FromMergeDiff — дифф реквеста от GitLab; широкий контекст — из файлов рабочего дерева.
func FromMergeDiff(files []*gl.MergeRequestDiff, baseRef, cwd string) (*Diff, error) {
	chunks := make([]string, len(files))
	names := make([]string, len(files))
	for i, f := range files {
		from, to := "--- a/"+f.OldPath, "+++ b/"+f.NewPath
		if f.NewFile {
			from = "--- /dev/null"
		}
		if f.DeletedFile {
			to = "+++ /dev/null"
		}
		chunks[i] = strings.Join([]string{"diff --git a/" + f.OldPath + " b/" + f.NewPath, from, to, strings.TrimSuffix(f.Diff, "\n")}, "\n")
		names[i] = f.NewPath
		if f.DeletedFile {
			names[i] = f.OldPath
		}
	}
	d, err := FromText(strings.Join(chunks, "\n"), baseRef, names)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.RenamedFile && f.OldPath != "" {
			d.renamedFrom[f.NewPath] = f.OldPath
		}
	}
	d.read = FileReader(cwd)
	d.cwd = cwd
	delete(d.widened, 0)
	return d, nil
}

// FromText — дифф из готового текста; files nil — файлы обеих сторон.
func FromText(text, baseRef string, files []string) (*Diff, error) {
	added, removed, err := ParseUnifiedDiff(text)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []string{}
		seen := map[string]bool{}
		for _, f := range append(append([]string{}, added.Files...), removed.Files...) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return &Diff{BaseRef: baseRef, Files: files, Added: added, Removed: removed, Revs: []string{baseRef, "HEAD"},
		widened: map[int]string{0: text}, renamedFrom: map[string]string{}, SourceText: &text}, nil
}

// NewDiff — дифф файлов между ревизиями с уже известным переименованием (команда `review diff`).
func NewDiff(baseRef string, files, revs []string, cwd string, renamedFrom map[string]string) *Diff {
	return &Diff{BaseRef: baseRef, Files: files, Added: newSide(), Removed: newSide(), Revs: revs, cwd: cwd,
		widened: map[int]string{}, renamedFrom: renamedFrom}
}

func (d *Diff) OldPathOf(file string) string {
	if from, ok := d.renamedFrom[file]; ok {
		return from
	}
	return file
}

func (d *Diff) IsEmpty() bool { return len(d.Files) == 0 }

func (d *Diff) ChangedLineCount() int {
	n := 0
	for _, f := range d.Added.Files {
		n += d.Added.ByFile[f].Len()
	}
	return n
}

func (d *Diff) side(deleted bool) *Side {
	if deleted {
		return d.Removed
	}
	return d.Added
}

func (d *Diff) Touches(file string, deleted bool) bool {
	_, ok := d.side(deleted).ByFile[file]
	return ok
}

func (d *Diff) HasLine(file string, line int, deleted bool) bool {
	lines := d.side(deleted).ByFile[file]
	if lines == nil {
		return false
	}
	_, ok := lines.ByNum[line]
	return ok
}

func (d *Diff) LinesOf(file string, deleted bool) []int {
	if lines := d.side(deleted).ByFile[file]; lines != nil {
		return lines.Order
	}
	return []int{}
}

// PairAt — старый и новый номер строки; ok=false — строки нет среди изменений.
func (d *Diff) PairAt(file string, line int, deleted bool) (oldLine, newLine int, ok bool) {
	lines := d.side(deleted).ByFile[file]
	if lines == nil {
		return 0, 0, false
	}
	entry, ok := lines.ByNum[line]
	if !ok {
		return 0, 0, false
	}
	if deleted {
		return line, entry.Other, true
	}
	return entry.Other, line, true
}

// Text — дифф с контекстом в context строк, таким, каким его печатает git.
func (d *Diff) Text(context int) (string, error) {
	if len(d.Files) == 0 {
		return "", nil
	}
	if text, ok := d.widened[context]; ok {
		return text, nil
	}
	var text string
	if d.read != nil {
		var err error
		if text, err = widenFromWorkTree(d.Files, d.Added, d.Removed, d.read, context); err != nil {
			return "", err
		}
	} else {
		// Старый путь в pathspec — иначе git не видит пару и показывает переименование добавлением файла.
		args := append([]string{"diff", "-U" + strconv.Itoa(context)}, d.Revs...)
		args = append(args, "--")
		seen := map[string]bool{}
		for _, f := range d.Files {
			for _, p := range []string{d.OldPathOf(f), f} {
				if !seen[p] {
					seen[p] = true
					args = append(args, p)
				}
			}
		}
		out, err := git.Git(args, d.cwd)
		if err != nil {
			return "", err
		}
		text = out + "\n"
	}
	d.widened[context] = text
	return text, nil
}

// Начало строки-комментария по синтаксису языка файла; в Markdown, тексте и данных комментариев нет.
func cLike(s string) bool {
	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "*")
}

func markup(s string) bool { return strings.HasPrefix(s, "<!--") || strings.HasPrefix(s, "-->") }

func hash(s string) bool { return strings.HasPrefix(s, "#") }

// shortcut: язык — по расширению или имени файла; не названный здесь язык комментариев не даёт — дописать, когда понадобится.
var commentSyntax = func() map[string]func(string) bool {
	m := map[string]func(string) bool{}
	for _, ext := range strings.Fields("js mjs cjs jsx ts tsx go java kt c h cpp hpp cs swift rs scala dart proto css scss less") {
		m[ext] = cLike
	}
	// `#[` в PHP — атрибут, а не комментарий.
	m["php"] = func(s string) bool { return cLike(s) || (hash(s) && !strings.HasPrefix(s, "#[")) }
	for _, ext := range []string{"vue", "html"} {
		m[ext] = func(s string) bool { return cLike(s) || markup(s) }
	}
	for _, ext := range []string{"xml", "svg"} {
		m[ext] = markup
	}
	m["sql"] = func(s string) bool { return strings.HasPrefix(s, "--") || cLike(s) }
	for _, ext := range strings.Fields("sh bash py rb pl r yaml yml toml ini conf neon tf dockerfile makefile gitignore htaccess") {
		m[ext] = hash
	}
	return m
}()

func commentSyntaxOf(file string) func(string) bool {
	parts := strings.Split(path.Base(file), ".")
	return commentSyntax[strings.ToLower(parts[len(parts)-1])]
}

// CommentLines — добавленные строки-комментарии файла по возрастанию.
type CommentLines struct {
	File    string
	Numbers []int
}

// AddedCommentLines — добавленные строки-комментарии по файлам.
func (d *Diff) AddedCommentLines() []CommentLines {
	var out []CommentLines
	for _, file := range d.Added.Files {
		comment := commentSyntaxOf(file)
		if comment == nil {
			continue
		}
		lines := d.Added.ByFile[file]
		var numbers []int
		for _, n := range lines.Order {
			if comment(strings.TrimSpace(lines.ByNum[n].Text)) {
				numbers = append(numbers, n)
			}
		}
		sort.Ints(numbers)
		if len(numbers) > 0 {
			out = append(out, CommentLines{file, numbers})
		}
	}
	return out
}
