package report

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gl "gitlab.com/gitlab-org/api/client-go"
	"go.yaml.in/yaml/v3"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/artifacts"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/gitlab"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/materials"
	"github.com/hakastein/llamer/internal/redact"
)

const (
	Version    = 3
	ReportFile = "report.md"
	ResultFile = "result.yaml"
)

// Failure — срыв прогона: причина, лимит подписки при rate_limit и подробность.
type Failure struct {
	Reason   string
	ResetsAt *int64
	Detail   string
}

const rateLimitHead = "Ревью не выполнено: исчерпан лимит подписки драйвера."

var failureTexts = map[string]string{
	"rate_limit": rateLimitHead + " Нужен перезапуск после того, как откроется следующее окно.",
	"auth":       "Ревью не выполнено: авторизация драйвера недействительна — её чинит тот, кто запускает.",
	"timeout": "Ревью не выполнено: этап не уложился в лимит времени и был остановлен." +
		" Найденное до остановки осталось в артефактах прогона — перезапусти его.",
	"died": "Ревью не выполнено: процесс этапа завершился, не отдав итог." +
		" Найденное до этого осталось в артефактах прогона — перезапусти его.",
	"scope": "Ревью не выполнено: не удалось отделить изменения задачи от чужих.",
	"other": "Ревью не выполнено из-за сбоя прогона.",
}

func FailureComment(f Failure) string {
	var text string
	if f.Reason == "rate_limit" && f.ResetsAt != nil && *f.ResetsAt != 0 {
		text = rateLimitHead + " Окно откроется " + agent.FormatResetTime(*f.ResetsAt) + " — после этого прогон нужно перезапустить."
	} else {
		key := f.Reason
		if strings.HasPrefix(key, "killed:") {
			key = "died"
		}
		var ok bool
		if text, ok = failureTexts[key]; !ok {
			text = failureTexts["other"]
		}
	}
	if f.Detail != "" {
		text += "\n\nПричина: " + f.Detail + "."
	}
	return text
}

// Request — то, что доставщики знают о запросе.
type Request interface {
	Kind() string
	HeadSha() string
	Label() string
}

// Delivery — доставщик результата прогона.
type Delivery interface {
	Deliver(r *Review) (unpublished []*domain.Finding, failures []string)
	DeliverNothingOwn(task *materials.TaskChanges, markSha string) []string
	DeliverFailure(f Failure, stages []*agent.Record) []string
}

// Результат прогона v3: поля и их порядок — контракт с запустившим агентом.
type (
	header struct {
		Version int    `yaml:"version"`
		Input   string `yaml:"input"`
		Head    string `yaml:"head"`
	}
	places struct {
		Report    string `yaml:"report"`
		Result    string `yaml:"result"`
		Artifacts string `yaml:"artifacts"`
	}
	reviewResult struct {
		header          `yaml:",inline"`
		OK              bool                     `yaml:"ok"`
		Base            string                   `yaml:"base"`
		Assessment      *assessmentResult        `yaml:"assessment"`
		Findings        []findingResult          `yaml:"findings"`
		Summaries       []summaryResult          `yaml:"summaries"`
		ResolvedThreads []resolvedResult         `yaml:"resolvedThreads"`
		Gaps            []string                 `yaml:"gaps"`
		Notices         []string                 `yaml:"notices"`
		Cost            agent.CostSummary        `yaml:"cost"`
		Limits          map[string]*agent.Limits `yaml:"limits"`
		Stages          []stageResult            `yaml:"stages"`
		places          `yaml:",inline"`
	}
	failureResult struct {
		header  `yaml:",inline"`
		OK      bool `yaml:"ok"`
		Failure struct {
			Reason string  `yaml:"reason"`
			Detail *string `yaml:"detail"`
			Text   string  `yaml:"text"`
		} `yaml:"failure"`
		Findings []findingResult          `yaml:"findings"`
		Limits   map[string]*agent.Limits `yaml:"limits"`
		places   `yaml:",inline"`
	}
	assessmentResult struct {
		Nature string `yaml:"nature"`
		Risk   string `yaml:"risk"`
		Level  int    `yaml:"level"`
		Reason string `yaml:"reason"`
	}
	// findingResult — находка в результате: то, что читает запустивший прогон агент.
	findingResult struct {
		ID             int              `yaml:"id"`
		Stage          string           `yaml:"stage"`
		Driver         string           `yaml:"driver"`
		File           string           `yaml:"file"`
		Line           int              `yaml:"line"`
		LineEnd        int              `yaml:"lineEnd"`
		Deleted        bool             `yaml:"deleted"`
		Severity       string           `yaml:"severity"`
		SeverityReason string           `yaml:"severityReason"`
		Text           string           `yaml:"text"`
		Suggestion     *string          `yaml:"suggestion"`
		Related        []domain.Related `yaml:"related"`
		Trace          *string          `yaml:"trace"`
	}
	summaryResult struct {
		Stage string `yaml:"stage"`
		Text  string `yaml:"text"`
	}
	resolvedResult struct {
		ID     string `yaml:"id"`
		Reason string `yaml:"reason"`
		Place  string `yaml:"place"`
	}
	// stageResult — что этап стоил и как продолжить его сессию: команда, к которой дописывают вопрос в кавычках.
	stageResult struct {
		Name   string   `yaml:"name"`
		Model  string   `yaml:"model"`
		Effort *string  `yaml:"effort"`
		OK     bool     `yaml:"ok"`
		USD    *float64 `yaml:"usd"`
		Resume *string  `yaml:"resume"`
	}
)

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stageResults — этапы в каждом результате ревью, даже если они не поднимались.
func stageResults(stages []*agent.Record) []stageResult {
	out := []stageResult{}
	for _, s := range stages {
		out = append(out, stageResult{Name: s.Name, Model: s.Model, Effort: optional(s.Effort), OK: s.OK, USD: s.Cost, Resume: optional(s.Resume)})
	}
	return out
}

func findingBlock(f *domain.Finding, index int) string {
	stage := f.Stage
	if stage == "" {
		stage = "?"
	}
	lines := []string{fmt.Sprintf("### %d. `%s` [%s] — этап %s", index+1, f.At(), f.Severity, stage), "", f.Text}
	if f.SeverityReason != "" {
		lines = append(lines, "", "Вес: "+f.SeverityReason)
	}
	if f.Suggestion != nil {
		lines = append(lines, "", "Предлагаемая замена:", "", "```", *f.Suggestion, "```")
	}
	if f.Trace != nil && *f.Trace != "" {
		lines = append(lines, "", "След: "+*f.Trace)
	}
	return strings.Join(lines, "\n")
}

// ReportDelivery — выход по умолчанию: YAML-результат в stdout и `result.yaml`,
// `report.md` из тех же данных — в артефактах прогона. Результат читает
// запустивший прогон агент, поэтому stdout несёт только его, а весь лог идёт в stderr.
type ReportDelivery struct {
	Request   Request
	Artifacts *artifacts.Run
	Stdout    io.Writer
}

// redactNode — строки результата без значений секретов. Вымарывается до
// YAML: на месте секрета в тексте YAML звёздочки прочлись бы ссылкой.
func redactNode(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		n.Value = redact.Secrets(n.Value)
	}
	for _, c := range n.Content {
		redactNode(c)
	}
}

// emit — build получает шапку и места файлов и отдаёт результат.
func (d *ReportDelivery) emit(markdown string, build func(header, places) any) error {
	report, err := d.Artifacts.SaveText(ReportFile, markdown+"\n")
	if err != nil {
		return err
	}
	result := build(header{Version, d.Request.Kind(), d.Request.HeadSha()},
		places{report, filepath.Join(d.Artifacts.Path, ResultFile), d.Artifacts.Path})
	var node yaml.Node
	if err := node.Encode(result); err != nil {
		return err
	}
	redactNode(&node)
	var text strings.Builder
	enc := yaml.NewEncoder(&text)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return err
	}
	if _, err := d.Artifacts.SaveText(ResultFile, text.String()); err != nil {
		return err
	}
	_, err = io.WriteString(d.Stdout, text.String())
	return err
}

func (d *ReportDelivery) title() string { return "# Ревью " + d.Request.Label() }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func (d *ReportDelivery) Deliver(r *Review) ([]*domain.Finding, []string) {
	findings, assessment := r.Findings(), r.Assessment()
	shortfall := r.Shortfall(nil)
	parts := []string{d.title(), "База `" + git.Short(r.BaseSha()) + "`, голова `" + git.Short(d.Request.HeadSha()) + "`. Артефакты прогона: " + d.Artifacts.Path}
	if note := r.RiskNote(); note != "" {
		parts = append(parts, "## Заметка о рисках\n\n"+note)
	}
	if len(findings) > 0 {
		blocks := make([]string, len(findings))
		for i, f := range findings {
			blocks[i] = findingBlock(f, i)
		}
		parts = append(parts, fmt.Sprintf("## Находки: %d\n\n%s", len(findings), strings.Join(blocks, "\n\n")))
	} else {
		parts = append(parts, "## Находок нет")
	}
	resolved := r.Journal.Resolved
	if len(resolved) > 0 {
		lines := make([]string, len(resolved))
		for i, t := range resolved {
			lines[i] = "- " + place(t) + " — " + t.Reason
		}
		parts = append(parts, "## Снятые треды\n\n"+strings.Join(lines, "\n"))
	}
	if summary := r.Summary(nil); summary != "" {
		parts = append(parts, "## Сводка\n\n"+summary)
	}
	if len(shortfall.Gaps) > 0 {
		parts = append(parts, "## Недостача\n\n"+bulleted(shortfall.Gaps))
	}
	var sessions []string
	for _, s := range r.Stages {
		if s.Resume != "" {
			sessions = append(sessions, "- "+s.Name+": `"+s.Resume+" '<вопрос>'`")
		}
	}
	if len(sessions) > 0 {
		parts = append(parts, "## Сессии этапов\n\nПродолжить сессию этапа вопросом к агенту:\n\n"+strings.Join(sessions, "\n"))
	}
	if len(r.Stages) > 0 {
		parts = append(parts, "## Цена\n\n```\n"+agent.FormatCostTable(r.Stages, r.ElapsedMs)+"\n```")
	}
	result := reviewResult{OK: true, Base: r.BaseSha(), Findings: []findingResult{}, Summaries: []summaryResult{}, ResolvedThreads: []resolvedResult{},
		Gaps: shortfall.Gaps, Notices: shortfall.Notices, Cost: agent.Summarize(r.Stages), Limits: agent.DriverLimits(r.Stages), Stages: stageResults(r.Stages)}
	if assessment != nil {
		result.Assessment = &assessmentResult{assessment.Nature, assessment.Risk, assessment.Level, assessment.Reason}
	}
	for _, f := range findings {
		result.Findings = append(result.Findings, findingResult{ID: f.ID, Stage: f.Stage, Driver: f.Driver, File: f.File, Line: f.Line, LineEnd: f.LineEnd,
			Deleted: f.Deleted, Severity: f.Severity, SeverityReason: f.SeverityReason, Text: f.Text, Suggestion: f.Suggestion, Related: f.Related, Trace: f.Trace})
	}
	for _, s := range r.Journal.Summaries {
		result.Summaries = append(result.Summaries, summaryResult{s.Stage, s.Text})
	}
	for _, t := range resolved {
		result.ResolvedThreads = append(result.ResolvedThreads, resolvedResult{t.ID, t.Reason, t.Place})
	}
	must(d.emit(strings.Join(parts, "\n\n"), func(h header, p places) any {
		result.header, result.places = h, p
		return result
	}))
	return nil, nil
}

func (d *ReportDelivery) DeliverNothingOwn(task *materials.TaskChanges, _ string) []string {
	must(d.emit(strings.Join([]string{d.title(), NothingOwnText(task), ScopeBlock(task)}, "\n\n"), func(h header, p places) any {
		return reviewResult{header: h, OK: true, Base: task.BaseSha, Findings: []findingResult{}, Summaries: []summaryResult{},
			ResolvedThreads: []resolvedResult{}, Gaps: []string{}, Notices: []string{}, Cost: agent.Summarize(nil),
			Limits: map[string]*agent.Limits{}, Stages: []stageResult{}, places: p}
	}))
	return nil
}

// DeliverFailure — сорванный прогон — тот случай, когда лимиты подписки интереснее всего: они в результате и здесь.
func (d *ReportDelivery) DeliverFailure(f Failure, stages []*agent.Record) []string {
	text := FailureComment(f)
	must(d.emit(d.title()+"\n\n"+text, func(h header, p places) any {
		result := failureResult{header: h, Findings: []findingResult{}, Limits: agent.DriverLimits(stages), places: p}
		result.Failure.Reason, result.Failure.Detail, result.Failure.Text = f.Reason, optional(f.Detail), text
		return result
	}))
	return nil
}

func place(t *domain.Resolve) string {
	if t.Place == "" {
		return t.ID
	}
	return t.Place
}

func bulleted(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "- " + l
	}
	return strings.Join(out, "\n")
}

// GitlabDelivery — `--publish gitlab`: единственный, кто знает порядок
// обращений к GitLab — заметка о рисках, закрытие тредов, треды находок, сводка, статус.
type GitlabDelivery struct {
	MR      *gitlab.MergeRequest
	RunPath string
	failed  []string
}

// attempt — label — готовый отказ («тред a.php:10 закрыть не удалось»):
// закрытие треда и публикация находки называют разные действия.
func (d *GitlabDelivery) attempt(label string, call func() error) bool {
	err := call()
	if err == nil {
		return true
	}
	entry := label + ": " + err.Error()
	d.failed = append(d.failed, entry)
	logx.Log(entry)
	// Каталог артефактов недоступен — терять из-за этого прогон незачем: отказ уже в логе и в итоге.
	if f, err := os.OpenFile(filepath.Join(d.RunPath, "publish-errors.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
		f.WriteString(redact.Secrets(entry) + "\n")
		f.Close()
	}
	return false
}

func (d *GitlabDelivery) postUnanchored(f *domain.Finding, r *Review) bool {
	return d.attempt("замечание "+f.At()+" опубликовать не удалось", func() error {
		if _, err := d.MR.PostDiscussion(gitlab.UnanchoredBody(f, r.MarkSha), nil); err != nil {
			return err
		}
		logx.Log("опубликовано замечание " + f.At() + ", без привязки к строкам")
		return nil
	})
}

func (d *GitlabDelivery) position(f *domain.Finding, r *Review) (*gl.PositionOptions, error) {
	// Новая сторона у изменений задачи и реквеста одна — голова ветки, а старая
	// у каждого своя: удалённые строки переносятся на нумерацию реквеста.
	inRequest := func(line int) (int, error) {
		if !f.Deleted {
			return line, nil
		}
		return DeletedLineInRequest(f.File, line, r.Materials.TaskAnchors, r.Materials.RequestDiff)
	}
	start, err := inRequest(f.Line)
	if err != nil {
		return nil, err
	}
	end, err := inRequest(f.LineEnd)
	if err != nil {
		return nil, err
	}
	version, err := d.MR.Version()
	if err != nil {
		return nil, err
	}
	return ThreadPosition(f.File, start, end, f.Deleted, r.Materials.RequestDiff, version)
}

// postFinding — замечание, которое не встало на строки, всё равно доезжает до
// автора — заметкой с местом в тексте.
func (d *GitlabDelivery) postFinding(f *domain.Finding, r *Review) bool {
	position, err := d.position(f, r)
	if err != nil {
		logx.Log(f.At() + ": " + err.Error() + " — публикуем заметкой")
		return d.postUnanchored(f, r)
	}
	var thread *gl.Discussion
	if !d.attempt("тред "+f.At()+" опубликовать не удалось", func() (err error) {
		thread, err = d.MR.PostDiscussion(gitlab.FindingBody(f, r.MarkSha), position)
		return err
	}) {
		return false
	}
	if err := AssertAnchored(thread, f.File, f.Line); err != nil {
		// Нота уже создана и висит в реквесте без места и с неприменимой правкой:
		// её сносим и повторяем заметкой.
		logx.Log(f.At() + ": " + err.Error() + " — заменяем заметкой")
		if len(thread.Notes) > 0 {
			noteID := thread.Notes[0].ID
			d.attempt("непривязанную ноту "+f.At()+" убрать не удалось", func() error { return d.MR.DeleteNote(noteID) })
		}
		return d.postUnanchored(f, r)
	}
	logx.Log("опубликован тред " + f.At())
	return true
}

func (d *GitlabDelivery) Deliver(r *Review) ([]*domain.Finding, []string) {
	if note := r.RiskNote(); note != "" {
		d.attempt("заметку о рисках опубликовать не удалось", func() error {
			if err := d.MR.PostNote(AssessmentMarker + "\n" + note); err != nil {
				return err
			}
			logx.Log("заметка о рисках опубликована")
			return nil
		})
	}
	for _, t := range r.Journal.Resolved {
		p := place(t)
		d.attempt("тред "+p+" закрыть не удалось", func() error {
			if err := d.MR.ResolveDiscussion(t.ID); err != nil {
				return err
			}
			logx.Log("закрыт тред " + p + " — " + t.Reason)
			return nil
		})
	}
	unpublished := []*domain.Finding{}
	for _, f := range r.Findings() {
		if !d.postFinding(f, r) {
			unpublished = append(unpublished, f)
		}
	}
	if summary := r.Summary(unpublished); summary != "" {
		d.attempt("сводку опубликовать не удалось", func() error {
			if err := d.MR.PostNote(gitlab.WithRevisionMark(summary, r.MarkSha)); err != nil {
				return err
			}
			logx.Log("опубликована сводка")
			return nil
		})
	}
	d.attempt("статус опубликовать не удалось", func() error { return d.MR.PostNote(r.Status(unpublished)) })
	return unpublished, d.failed
}

func (d *GitlabDelivery) DeliverNothingOwn(task *materials.TaskChanges, markSha string) []string {
	// Пустая дельта после прошлого прогона: его статус в MR остаётся в силе, новая заметка — шум.
	if task.Since != "" {
		logx.Log("после прошлого прогона " + git.Short(task.Since) + " новых своих изменений нет — в MR ничего не публикуется")
		return d.failed
	}
	d.attempt("заметку о пропуске ревью опубликовать не удалось", func() error { return d.MR.PostNote(nothingOwnNote(task, markSha)) })
	return d.failed
}

func (d *GitlabDelivery) DeliverFailure(f Failure, _ []*agent.Record) []string {
	body := gitlab.StatusMarker + "\n\n" + FailureComment(f)
	d.attempt("сообщение о срыве опубликовать не удалось", func() error { return d.MR.PostNote(body) })
	return d.failed
}
