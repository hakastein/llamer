package report

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/hakastein/llamer/internal/materials"
)

// lineCode — код строки диффа в терминах GitLab: `sha1(путь)_<старая>_<новая>`.
// Пару даёт разбор диффа — у добавленной строки старый номер это счётчик
// старого файла на её позиции, у удалённой наоборот.
func lineCode(file string, oldLine, newLine int) string {
	sum := sha1.Sum([]byte(file))
	return fmt.Sprintf("%s_%d_%d", hex.EncodeToString(sum[:]), oldLine, newLine)
}

// ThreadPosition — позиция инлайн-треда. Комментарий всегда несёт диапазон:
// GitLab ставит ноту на последнюю строку и подсвечивает от line_range.start до
// неё; диапазон в одну строку — частный случай той же разметки.
func ThreadPosition(file string, start, end int, deleted bool, diff *materials.Diff, version *gl.MergeRequestDiffVersion) (*gl.PositionOptions, error) {
	for _, number := range []int{start, end} {
		if !diff.HasLine(file, number, deleted) {
			side := "изменённых"
			if deleted {
				side = "удалённых"
			}
			return nil, fmt.Errorf("строка %s:%d не найдена среди %s — код строки для GitLab не собрать", file, number, side)
		}
	}
	anchor := func(number int) *gl.LinePositionOptions {
		oldLine, newLine, _ := diff.PairAt(file, number, deleted)
		a := &gl.LinePositionOptions{LineCode: gl.Ptr(lineCode(file, oldLine, newLine)), Type: gl.Ptr("new"), NewLine: gl.Ptr(int64(number))}
		if deleted {
			a.Type, a.OldLine, a.NewLine = gl.Ptr("old"), gl.Ptr(int64(number)), nil
		}
		return a
	}
	p := &gl.PositionOptions{PositionType: gl.Ptr("text"), BaseSHA: gl.Ptr(version.BaseCommitSHA), HeadSHA: gl.Ptr(version.HeadCommitSHA),
		StartSHA: gl.Ptr(version.StartCommitSHA), NewPath: gl.Ptr(file), OldPath: gl.Ptr(diff.OldPathOf(file)),
		LineRange: &gl.LineRangeOptions{Start: anchor(start), End: anchor(end)}}
	if deleted {
		p.OldLine = gl.Ptr(int64(end))
	} else {
		p.NewLine = gl.Ptr(int64(end))
	}
	return p, nil
}

// AssertAnchored — GitLab не отказывает на непонятой позиции, а создаёт обычный
// тред к MR целиком: публикация выглядит успешной, а комментарий висит ни на
// какой строке, и правка в нём не применяется кнопкой.
func AssertAnchored(thread *gl.Discussion, file string, line int) error {
	var note *gl.Note
	if len(thread.Notes) > 0 {
		note = thread.Notes[0]
	}
	if note == nil || note.Type != "DiffNote" || note.Position == nil || note.Position.LineRange == nil {
		kind := "неизвестен"
		if note != nil && note.Type != "" {
			kind = string(note.Type)
		}
		return fmt.Errorf("тред %s:%d создан без привязки к строкам (тип %s): GitLab не принял позицию. Предложенная правка в таком треде не применяется",
			file, line, kind)
	}
	return nil
}

func removedAt(diff *materials.Diff, file string, newLine int) []int {
	lines := diff.Removed.Get(file)
	var out []int
	if lines == nil {
		return out
	}
	for _, n := range lines.Order {
		if lines.ByNum[n].Other == newLine {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// DeletedLineInRequest — удалённая строка в изменениях задачи пронумерована от
// их базы, а тред встаёт на старую сторону диффа реквеста — от другой базы. В
// обоих диффах она стоит перед той же новой строкой и несёт тот же текст; блок
// одинаковых удалённых строк сопоставляется по порядку.
func DeletedLineInRequest(file string, line int, taskAnchors, requestDiff *materials.Diff) (int, error) {
	ours := taskAnchors.Removed.Get(file)
	var entry materials.Line
	found := false
	if ours != nil {
		entry, found = ours.ByNum[line]
	}
	if !found {
		return 0, fmt.Errorf("удалённой строки %s:%d нет в изменениях задачи", file, line)
	}
	ourBlock := removedAt(taskAnchors, file, entry.Other)
	theirBlock := removedAt(requestDiff, file, entry.Other)
	theirs := requestDiff.Removed.Get(file)
	same := len(ourBlock) == len(theirBlock)
	for i := 0; same && i < len(ourBlock); i++ {
		same = ours.ByNum[ourBlock[i]].Text == theirs.ByNum[theirBlock[i]].Text
	}
	if same {
		return theirBlock[slices.Index(ourBlock, line)], nil
	}
	var matches []int
	for _, n := range theirBlock {
		if theirs.ByNum[n].Text == entry.Text {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return 0, errors.New("удалённая строка " + file + ":" + fmt.Sprint(line) + " не найдена в диффе реквеста")
}
