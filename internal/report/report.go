// Package report — что прогон произвёл, и его доставка: отчёт и YAML-результат
// или публикация в GitLab.
package report

import (
	"fmt"
	"strings"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/materials"
)

// PluralRu — форма русского слова по числу: файл, файла, файлов.
func PluralRu(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return many
	case mod10 == 1:
		return one
	case mod10 >= 2 && mod10 <= 4:
		return few
	}
	return many
}

const AssessmentMarker = "<!-- llamer-assessment -->"

var (
	natureTitles = map[string]string{"mechanical": "механическое", "behavioral": "содержательное"}
	riskTitles   = map[string]string{"low": "низкий", "medium": "средний", "high": "высокий"}
)

func titled(titles map[string]string, v string) string {
	if t, ok := titles[v]; ok {
		return t
	}
	return v
}

func riskNote(assessment *domain.Assessment, signals materials.Signals, reviewed string) string {
	parts := []string{
		"**Оценка перед разбором**", "",
		"Изменение " + titled(natureTitles, assessment.Nature) + ", риск " + titled(riskTitles, assessment.Risk) + ".", "",
		"Обоснование: " + assessment.Reason, "",
		fmt.Sprintf("Разбор идёт на уровне %d из %d.", assessment.Level, config.LevelCount), "",
		signals.Summary(),
	}
	if reviewed != "" {
		parts = append(parts, "", reviewed)
	}
	return strings.Join(parts, "\n")
}

func files(n int) string {
	return fmt.Sprintf("%d %s", n, PluralRu(n, "файл", "файла", "файлов"))
}

// ScopeBlock — сворачиваемый блок «что разбиралось и от чего»: читатель видит,
// почему часть файлов реквеста ревьюер не тронул, и может повторить дифф руками.
func ScopeBlock(task *materials.TaskChanges) string {
	diff := task.Diff
	conflicted := map[string]bool{}
	for _, p := range task.Conflicted {
		conflicted[p] = true
	}
	parts := []string{"<details><summary>Что ревьюилось: " + files(len(diff.Files)) + "</summary>", ""}
	if task.Kind == "explicit" {
		parts = append(parts, "Дифф от базы `"+git.Short(task.Bases[0].Sha)+"`, заданной `--base`.")
	} else {
		revs := make([]string, len(task.Bases))
		for i, b := range task.Bases {
			revs[i] = "`" + b.Rev + "`"
		}
		parts = append(parts, "Изменения задачи: всё, чего ещё нет в "+strings.Join(revs, ", ")+".")
		bases := make([]string, len(task.Bases))
		for i, b := range task.Bases {
			bases[i] = "`" + b.Rev + "@" + git.Short(b.Sha) + "`"
		}
		parts = append(parts, "", "Базы: "+strings.Join(bases, ", ")+".")
	}
	if n := len(conflicted); n > 0 {
		parts = append(parts, "", "Между исключёнными ревизиями "+PluralRu(n, "конфликтует", "конфликтуют", "конфликтуют")+
			" "+files(n)+": по ним разобран дифф реквеста.")
	}
	if len(diff.Files) > 0 {
		parts = append(parts, "")
		for _, f := range diff.Files {
			line := fmt.Sprintf("- `%s` +%d −%d", f, diff.Added.Get(f).Len(), diff.Removed.Get(f).Len())
			if conflicted[f] {
				line += " — по диффу реквеста"
			}
			parts = append(parts, line)
		}
	}
	if len(task.Excluded) > 0 {
		kind := "чужие"
		if task.Since != "" {
			kind = "чужие или уже разобранные прошлым прогоном"
		}
		parts = append(parts, "", "Отсеяно как "+kind+" изменения: "+files(len(task.Excluded))+" диффа реквеста.", "",
			"<details><summary>Отсеянные файлы</summary>", "")
		for _, f := range task.Excluded {
			parts = append(parts, "- `"+f+"`")
		}
		parts = append(parts, "", "</details>")
	}
	return strings.Join(append(parts, "", "</details>"), "\n")
}

// NothingOwnText — почему ревью пропущено: с `--since-last` пусто может быть и в дельте после прошлого прогона.
func NothingOwnText(task *materials.TaskChanges) string {
	if task.Since != "" {
		return "Новых своих изменений после прошлого прогона нет. Ревью пропущено."
	}
	return "Разбирать нечего: голова не добавляет изменений к базе разбора. Ревью пропущено."
}

// nothingOwnNote — изменения без своего кода: этапы не поднимаются, но
// молчание ревьюера читалось бы как сбой.
func nothingOwnNote(task *materials.TaskChanges, markSha string) string {
	return gitlab.WithRevisionMark(gitlab.StatusMarker+"\n_"+NothingOwnText(task)+"_\n\n"+ScopeBlock(task), markSha)
}

// Shortfall — недостача прогона: в отчёт, автору и в счёт сорванных этапов.
type Shortfall struct {
	Gaps, Author, Notices []string
	Failed                int
}

// collectShortfall — сорванный экземпляр этапа, чей этап отработал на другом
// драйвере, — не недостача: разбор этапа есть. О нём сообщается, прогон не проваливается.
func collectShortfall(state *domain.State, unpublished []*domain.Finding, records []*agent.Record) Shortfall {
	s := Shortfall{Gaps: []string{}, Author: []string{}, Notices: []string{}}
	done := map[string]bool{}
	for _, r := range records {
		if r.OK {
			done[r.Key] = true
		}
	}
	for _, stop := range state.Stopped {
		name := stop.Name
		if name == "" {
			name = stop.Stage
		}
		s.Gaps = append(s.Gaps, "этап «"+name+"» прекратил работу: "+stop.Reason)
	}
	for _, f := range state.Failures {
		line := "этап «" + f.Stage + "» сорвался: " + f.Reason
		if done[f.Key] {
			notice := line + " — этап «" + f.Key + "» отработал на другом драйвере"
			s.Notices = append(s.Notices, notice)
			s.Author = append(s.Author, notice)
		} else {
			s.Failed++
			s.Gaps = append(s.Gaps, line)
		}
	}
	var noVerdict []string
	for _, f := range state.Findings {
		if f.AwaitsVerdict() {
			noVerdict = append(noVerdict, fmt.Sprintf("#%d", f.ID))
		}
	}
	if len(noVerdict) > 0 {
		s.Gaps = append(s.Gaps, fmt.Sprintf("скептик не проверил находок: %d (%s)", len(noVerdict), strings.Join(noVerdict, ", ")))
	}
	if state.Assessment == nil {
		s.Gaps = append(s.Gaps, "вердикта оценщика в журнале нет")
	}
	if state.SkippedLines > 0 {
		s.Gaps = append(s.Gaps, fmt.Sprintf("битых строк журнала: %d — столько записей потеряно", state.SkippedLines))
	}
	if len(unpublished) > 0 {
		places := make([]string, len(unpublished))
		for i, f := range unpublished {
			places[i] = f.At()
		}
		line := fmt.Sprintf("не опубликовано замечаний: %d (%s)", len(unpublished), strings.Join(places, ", "))
		s.Gaps = append(s.Gaps, line)
		s.Author = append(s.Author, line)
	}
	return s
}

func summaryComment(summaries []*domain.Summary, gaps []string) string {
	var parts []string
	for _, s := range summaries {
		if text := strings.TrimSpace(s.Text); text != "" {
			parts = append(parts, "**"+s.Stage+"**: "+text)
		}
	}
	if len(gaps) > 0 {
		lines := []string{"### Ревью неполное", ""}
		for _, g := range gaps {
			lines = append(lines, "- "+g)
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// CollectDropped — снятое дедупликатором и скептиком в реквест не едет: это
// единственный след, по которому видно, не перестарались ли они.
func CollectDropped(findings []*domain.Finding) []string {
	var lines, duplicates, refuted []string
	for _, f := range findings {
		if f.DuplicateOf != 0 {
			duplicates = append(duplicates, fmt.Sprintf("  %s:%d — повтор #%d // %s", f.File, f.Line, f.DuplicateOf, f.DuplicateReason))
		} else if f.Refuted {
			refuted = append(refuted, fmt.Sprintf("  %s:%d — %s // %s", f.File, f.Line, f.Text, f.RefuteReason))
		}
	}
	if len(duplicates) > 0 {
		lines = append(append(lines, fmt.Sprintf("дедупликация сняла находок: %d", len(duplicates))), duplicates...)
	}
	if len(refuted) > 0 {
		lines = append(append(lines, fmt.Sprintf("скептик опроверг находок: %d", len(refuted))), refuted...)
	}
	return lines
}

// Review — что прогон произвёл: данные, ни сети, ни печати. Оба доставщика
// читают одно и то же, поэтому расходиться в текстах им нечем.
type Review struct {
	Journal   *domain.State
	Materials *materials.Materials
	Stages    []*agent.Record
	MarkSha   string
	ElapsedMs int64

	repeats []string
	kept    []*domain.Finding
	split   bool
}

func (r *Review) Assessment() *domain.Assessment { return r.Journal.Assessment }

func (r *Review) RiskNote() string {
	if r.Assessment() == nil {
		return ""
	}
	return riskNote(r.Assessment(), r.Materials.Signals, ScopeBlock(r.Materials.Task))
}

func (r *Review) BaseSha() string { return r.Materials.BaseSha() }

// Findings — дословный повтор не должен стать двумя тредами на одной строке:
// этапы пишут независимо, а повторить запись агент может и сам. Разные
// замечания на одной строке остаются оба.
func (r *Review) Findings() []*domain.Finding {
	r.doSplit()
	return r.kept
}

func (r *Review) Repeats() []string {
	r.doSplit()
	return r.repeats
}

func (r *Review) doSplit() {
	if r.split {
		return
	}
	r.split = true
	seen := map[string]bool{}
	for _, f := range r.Journal.Surviving() {
		if seen[f.Key()] {
			r.repeats = append(r.repeats, "находка "+f.At()+" уже есть в этом прогоне — дословный повтор снят")
			continue
		}
		seen[f.Key()] = true
		r.kept = append(r.kept, f)
	}
}

func (r *Review) Shortfall(unpublished []*domain.Finding) Shortfall {
	return collectShortfall(r.Journal, unpublished, r.Stages)
}

// Summary — пустая сводка не должна стать пустым комментарием в MR.
func (r *Review) Summary(unpublished []*domain.Finding) string {
	return summaryComment(r.Journal.Summaries, r.Shortfall(unpublished).Author)
}

func (r *Review) Status(unpublished []*domain.Finding) string {
	status := "_Ревью выполнено._"
	if n := len(unpublished); n > 0 {
		status = fmt.Sprintf("_Ревью выполнено, %d %s опубликовать не удалось — см. сводку._", n, PluralRu(n, "замечание", "замечания", "замечаний"))
	}
	stats := agent.FormatRunStats(r.Stages, r.ElapsedMs)
	// Метка статуса — точка отсчёта `--since-last`: с сорванным или прекращённым
	// этапом или неопубликованными находками она дала бы следующему прогону пропустить неразобранное.
	mark := ""
	if len(unpublished) == 0 && r.Shortfall(unpublished).Failed == 0 && len(r.Journal.Stopped) == 0 {
		mark = r.MarkSha
	}
	return gitlab.WithRevisionMark(gitlab.StatusMarker+"\n"+status+"\n\n"+stats, mark)
}
