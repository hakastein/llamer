package materials

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"
)

var numberedHunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// Шире пяти знаков номера строк в репозитории не бывает; ровная колонка
// держит маркер `+`/`-` на одном месте, и строку читать как обычный дифф.
const numberWidth = 5

// NumberDiffLines — дифф пакета с номером строки текущего дерева у каждой
// строки ханка: готовый `path:line` для инструментов навигации и для `review`.
// У удалённой строки в дереве нет места, номер ей не ставится.
func NumberDiffLines(text string) string {
	line := -1
	rows := strings.Split(text, "\n")
	for i, row := range rows {
		if strings.HasPrefix(row, "diff --git ") {
			line = -1
			continue
		}
		if m := numberedHunk.FindStringSubmatch(row); m != nil {
			line, _ = strconv.Atoi(m[1])
			continue
		}
		if line < 0 || strings.HasPrefix(row, `\`) {
			continue
		}
		switch {
		case strings.HasPrefix(row, "-"):
			rows[i] = strings.Repeat(" ", numberWidth) + " " + row
		case strings.HasPrefix(row, "+") || strings.HasPrefix(row, " "):
			rows[i] = fmt.Sprintf("%*d %s", numberWidth, line, row)
			line++
		}
	}
	return strings.Join(rows, "\n")
}

// MatchesAnyMask — файл подходит, если подходит под маску без `!` и ни под
// одну с `!`, как в `.gitignore`. Без масок без `!` подходит всё. `**` покрывает
// любое число сегментов, `*` — часть одного: `src/*.php` не достаёт
// `src/deep/A.php`. Маски проверены при чтении конфига.
func MatchesAnyMask(masks, files []string) bool {
	var include, exclude []string
	for _, m := range masks {
		if strings.HasPrefix(m, "!") {
			exclude = append(exclude, m[1:])
		} else {
			include = append(include, m)
		}
	}
	hits := func(list []string, file string) bool {
		for _, m := range list {
			if doublestar.MatchUnvalidated(m, file) {
				return true
			}
		}
		return false
	}
	for _, f := range files {
		if (len(include) == 0 || hits(include, f)) && !hits(exclude, f) {
			return true
		}
	}
	return false
}

// Конфиги инструментов массовой автоправки: их правка в реквесте — признак механики.
var mechanicalConfigs = []string{"rector.php", ".php-cs-fixer.dist.php", ".php-cs-fixer.php"}

var (
	hunkSplit   = regexp.MustCompile(`(?m)^@@[^\n]*\n`)
	addedLine   = regexp.MustCompile(`^\+[^+]`)
	removedLine = regexp.MustCompile(`^-[^-]`)
	shapeWord   = regexp.MustCompile(`[\wА-я]+`)
)

// MechanicalShare — доля однотипных однострочных ханков: 300 файлов с одним
// паттерном — одна правка, повторённая 300 раз. Вход — `git diff -U0`.
func MechanicalShare(diff string) float64 {
	hunks := hunkSplit.Split(diff, -1)[1:]
	if len(hunks) == 0 {
		return 0
	}
	shapes := map[string]int{}
	single := 0
	for _, hunk := range hunks {
		var added []string
		removed := 0
		for _, line := range strings.Split(hunk, "\n") {
			if addedLine.MatchString(line) {
				added = append(added, line)
			}
			if removedLine.MatchString(line) {
				removed++
			}
		}
		if len(added) != 1 || removed > 1 {
			continue
		}
		single++
		shapes[strings.TrimSpace(shapeWord.ReplaceAllString(added[0], "x"))]++
	}
	if single == 0 {
		return 0
	}
	repeated := 0
	for _, n := range shapes {
		if n > 1 {
			repeated += n
		}
	}
	return float64(repeated) / float64(len(hunks))
}

// Signals — признаки риска изменений.
type Signals struct {
	Files, ChangedLines int
	Share               float64
	Configs             []string
	GeneratedMarkers    int
	HasTests            bool
}

func (s Signals) Summary() string {
	configs := strings.Join(s.Configs, ", ")
	if configs == "" {
		configs = "нет"
	}
	tests := "нет"
	if s.HasTests {
		tests = "есть"
	}
	// Половина сотой — вверх: 0.125 → 0.13.
	share := strconv.FormatFloat(math.Floor(s.Share*100+0.5)/100, 'f', 2, 64)
	return fmt.Sprintf("Файлов %d, изменённых строк %d. Тесты в реквесте: %s.\nПризнаки механической правки: доля однотипных ханков %s, конфиги %s, маркеров @generated %d.",
		s.Files, s.ChangedLines, tests, share, configs, s.GeneratedMarkers)
}

// CollectRiskSignals — размер, механика и тесты изменений.
func CollectRiskSignals(diff *Diff) (Signals, error) {
	narrow, err := diff.Text(0)
	if err != nil {
		return Signals{}, err
	}
	s := Signals{Files: len(diff.Files), ChangedLines: diff.ChangedLineCount(), Share: MechanicalShare(narrow), Configs: []string{}}
	for _, f := range diff.Files {
		parts := strings.Split(f, "/")
		for _, c := range mechanicalConfigs {
			if parts[len(parts)-1] == c {
				s.Configs = append(s.Configs, f)
			}
		}
		// Сегмент пути, а не префикс от корня: тесты лежат и не под корневым tests/.
		for _, p := range parts {
			if p == "tests" {
				s.HasTests = true
			}
		}
	}
	// Маркер — среди ДОБАВЛЕННЫХ строк: в контекстных он принадлежит чужому коду.
	for _, f := range diff.Added.Files {
		for _, line := range diff.Added.ByFile[f].ByNum {
			if strings.Contains(line.Text, "@generated") {
				s.GeneratedMarkers++
			}
		}
	}
	return s, nil
}

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

// restoreOldVersion — старая версия файла из новой и узкого диффа.
func restoreOldVersion(newLines []string, added, removed *Lines) []string {
	type kept struct {
		newLine int
		text    string
	}
	var keep []kept
	for line := 1; line <= len(newLines); line++ {
		if _, ok := added.ByNum[line]; !ok {
			keep = append(keep, kept{line, newLines[line-1]})
		}
	}
	nums := append([]int{}, removed.Order...)
	sort.Ints(nums)
	var old []string
	taken := 0
	for _, n := range nums {
		entry := removed.ByNum[n]
		for taken < len(keep) && keep[taken].newLine <= entry.Other {
			old = append(old, keep[taken].text)
			taken++
		}
		old = append(old, entry.Text)
	}
	for ; taken < len(keep); taken++ {
		old = append(old, keep[taken].text)
	}
	return old
}

func gitDiffFiles(oldPath, newPath string, context int) (string, error) {
	out, err := exec.Command("git", "diff", "--no-index", "-U"+strconv.Itoa(context), oldPath, newPath).Output()
	if err == nil {
		return "", nil
	}
	// Различия git отдаёт кодом 1 — текст диффа лежит в stdout.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return string(out), nil
	}
	return "", err
}

func retitle(diff, file string, born bool) string {
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "similarity "):
			continue
		case strings.HasPrefix(line, "diff --git "):
			line = "diff --git a/" + file + " b/" + file
		case strings.HasPrefix(line, "--- "):
			line = "--- a/" + file
			if born {
				line = "--- /dev/null"
			}
		case strings.HasPrefix(line, "+++ "):
			line = "+++ b/" + file
		}
		out = append(out, line)
	}
	return strings.TrimRightFunc(strings.Join(out, "\n"), unicode.IsSpace)
}

func widenFromWorkTree(files []string, added, removed *Side, read func(string) (string, bool), context int) (string, error) {
	dir, err := os.MkdirTemp("", "review-widen-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	var chunks []string
	for _, file := range files {
		text, ok := read(file)
		if !ok {
			continue
		}
		newLines := splitLines(text)
		addedHere, removedHere := added.Get(file), removed.Get(file)
		if addedHere == nil {
			addedHere = &Lines{ByNum: map[int]Line{}}
		}
		if removedHere == nil {
			removedHere = &Lines{ByNum: map[int]Line{}}
		}
		if addedHere.Len() == 0 && removedHere.Len() == 0 {
			continue
		}
		oldPath, newPath := filepath.Join(dir, "old"), filepath.Join(dir, "new")
		oldLines := restoreOldVersion(newLines, addedHere, removedHere)
		oldText := ""
		if len(oldLines) > 0 {
			oldText = strings.Join(oldLines, "\n") + "\n"
		}
		if err := os.WriteFile(oldPath, []byte(oldText), 0o666); err != nil {
			return "", err
		}
		if err := os.WriteFile(newPath, []byte(strings.Join(newLines, "\n")+"\n"), 0o666); err != nil {
			return "", err
		}
		diff, err := gitDiffFiles(oldPath, newPath, context)
		if err != nil {
			return "", err
		}
		if chunk := retitle(diff, file, len(oldLines) == 0); chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	return strings.Join(chunks, "\n"), nil
}

const (
	findingPad     = 3
	windowFallback = 6
	contextCap     = 40
)

// FileReader — чтение файлов дерева с кэшем; ok=false — файл не читается.
func FileReader(cwd string) func(string) (string, bool) {
	type entry struct {
		text string
		ok   bool
	}
	cache := map[string]entry{}
	return func(file string) (string, bool) {
		e, seen := cache[file]
		if !seen {
			data, err := os.ReadFile(filepath.Join(cwd, file))
			e = entry{string(data), err == nil}
			cache[file] = e
		}
		return e.text, e.ok
	}
}

func lineAt(lines []string, n int) string {
	if n >= 1 && n <= len(lines) {
		return lines[n-1]
	}
	return ""
}

// ExtractFindingContext — окно строк вокруг находки.
func ExtractFindingContext(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	if end == 0 {
		end = start
	}
	var body []string
	for i := max(1, start-findingPad); i <= min(len(lines), end+findingPad); i++ {
		body = append(body, fmt.Sprintf("L%d\t%s", i, lineAt(lines, i)))
	}
	return strings.Join(body, "\n")
}

func braceBodyEnd(lines []string, from, limit int) int {
	depth, seen, end := 0, false, from-1
	for i := from; i <= limit; i++ {
		for _, c := range lineAt(lines, i) {
			switch c {
			case '{':
				depth++
				seen = true
			case '}':
				depth--
			}
		}
		end = i
		if seen && depth <= 0 {
			break
		}
	}
	return end
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// ExtractCommentContext — фрагменты «комментарий + код под ним»: смежные
// строки-комментарии (разрыв ≤1) образуют шапку, за ней — тело до закрытия
// скобки либо окно в windowFallback строк. Пометка `L<n>+` обязательна:
// комментарий, чья шапка попала внутрь окна предыдущего фрагмента, иначе
// неотличим от старого окружения.
func ExtractCommentContext(text string, commentLines []int) string {
	lines := strings.Split(text, "\n")
	added := map[int]bool{}
	for _, n := range commentLines {
		added[n] = true
	}
	sorted := append([]int{}, commentLines...)
	sort.Ints(sorted)
	type block struct{ start, end int }
	var blocks []*block
	for _, n := range sorted {
		if len(blocks) > 0 && n <= blocks[len(blocks)-1].end+1 {
			blocks[len(blocks)-1].end = n
		} else {
			blocks = append(blocks, &block{n, n})
		}
	}
	var fragments []string
	prevEnd := 0
	for _, b := range blocks {
		bodyStart := b.end + 1
		for bodyStart <= len(lines) && isBlank(lineAt(lines, bodyStart)) {
			bodyStart++
		}
		opens := false
		if bodyStart <= len(lines) && bodyStart <= b.end+3 {
			between := strings.Join(lines[min(b.end, len(lines)):min(bodyStart, len(lines))], "")
			opens = strings.Contains(between+lineAt(lines, bodyStart), "{")
		}
		var end int
		if opens {
			end = braceBodyEnd(lines, bodyStart, min(len(lines), b.start+contextCap))
		} else {
			end = min(len(lines), b.end+windowFallback)
		}
		start := max(prevEnd+1, b.start)
		if start > end {
			continue
		}
		var body []string
		for i := start; i <= end; i++ {
			mark := ""
			if added[i] {
				mark = "+"
			}
			body = append(body, fmt.Sprintf("L%d%s\t%s", i, mark, lineAt(lines, i)))
		}
		fragments = append(fragments, strings.Join(body, "\n"))
		prevEnd = end
	}
	return strings.Join(fragments, "\n  …\n")
}

// CommentsContext — добавленные комментарии с кодом под ними: материал этапа комментариев.
func CommentsContext(diff *Diff, cwd string) string {
	read := FileReader(cwd)
	var parts []string
	for _, c := range diff.AddedCommentLines() {
		text, ok := read(c.File)
		if !ok {
			continue
		}
		if fragment := ExtractCommentContext(text, c.Numbers); fragment != "" {
			parts = append(parts, c.File+"\n"+fragment)
		}
	}
	return strings.Join(parts, "\n\n")
}
