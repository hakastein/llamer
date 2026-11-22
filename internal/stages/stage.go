package stages

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/materials"
	artifacts "github.com/hakastein/llamer/internal/reviewartifacts"
	"github.com/hakastein/smith/agent"
	"github.com/hakastein/smith/cost"
	"github.com/hakastein/smith/drivers"
	"github.com/hakastein/smith/logx"
	"github.com/hakastein/smith/skills"
)

// Toolkit — то, чем этапы прогона пользуются сообща.
type Toolkit struct {
	Context   context.Context
	Artifacts *artifacts.Run
	Drivers   map[string]drivers.Driver
	Materials *materials.Materials
	Library   *skills.Library
	Config    *config.Config
	Shared    string // файл общей части пакета
	protocol  protocol
}

// findingCommands — пять команд записи находок нужны одинаково всем, кто их пишет.
var findingCommands = []string{"finding", "amend", "batch", "list", "blame"}

// Stage — экземпляр этапа прогона на одном драйвере: свой промпт, уровень на
// лестнице драйвера и набор команд харнесса. Служебный этап идёт единственным
// драйвером из конфига. Общаются этапы только журналом прогона.
type Stage struct {
	tk              *Toolkit
	settings        *config.Stage
	driver          string
	name            string
	templateKey     string
	projectMemory   bool
	relatedRequired bool
	commands        []string
	applies         func(m *materials.Materials, journal *domain.State) bool
	skipReason      string
	values          func(m *materials.Materials, journal *domain.State) map[string]string
	threads         bool // в задании — открытые треды

	journal       *domain.Journal
	record        *cost.Stage
	tmpl          *template
	responding    chan struct{}
	markResponded sync.Once
}

func newStage(tk *Toolkit, settings *config.Stage, driver string) *Stage {
	if driver == "" {
		driver = settings.Drivers[0]
	}
	return &Stage{tk: tk, settings: settings, driver: driver, templateKey: settings.Key, relatedRequired: true,
		applies: func(*materials.Materials, *domain.State) bool { return true }, responding: make(chan struct{})}
}

// ID — имя экземпляра: ключ этапа, у этапа на нескольких драйверах — с драйвером.
func (s *Stage) ID() string { return config.InstanceName(s.settings, s.driver) }

// Name — имя этапа в логе и артефактах.
func (s *Stage) Name() string {
	if s.name != "" {
		return s.name
	}
	return s.ID()
}

// Journal — журнал открывается по первому обращению.
func (s *Stage) Journal() *domain.Journal {
	if s.journal == nil {
		s.journal = s.tk.Artifacts.OpenJournal(s.ID())
	}
	return s.journal
}

// Record — запись отработавшего этапа; nil у не поднимавшегося.
func (s *Stage) Record() *cost.Stage { return s.record }

func (s *Stage) State() *domain.State { return s.Journal().State() }

// Responding — закрывается, когда модель начала отвечать этапу — с этого
// момента его промпт лежит в кэше, — или когда этап закончился, не дойдя до
// ответа: ждущий не должен висеть.
func (s *Stage) Responding() <-chan struct{} { return s.responding }

func (s *Stage) markResponding() { s.markResponded.Do(func() { close(s.responding) }) }

func (s *Stage) allow() []string {
	out := []string{harness.Name}
	for _, a := range append(slices.Clone(s.tk.Config.Allow), s.settings.Allow...) {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func (s *Stage) template() *template {
	if s.tmpl == nil {
		s.tmpl = loadTemplate(s.templateKey)
	}
	return s.tmpl
}

// style — как задание этапа пишется под CLI его драйвера: как этап берёт скиллы
// метода, как скилл назван в задании и как пишется одинарная кавычка внутри
// текста в вызове.
type style struct {
	skillsHow string
	skillRef  func(name string) string
	quote     string
}

var styles = map[string]style{
	// Скиллы этапа claude получает плагином `stage`: имя плагина — пространство имён скиллов в Skill.
	"claude": {
		skillsHow: "Load each of these with the Skill tool before you start.",
		skillRef:  func(name string) string { return "stage:" + name },
		quote:     `'\''`,
	},
	// Скиллы этапа codex находит сам в `$HOME/.agents/skills`, а упоминание `$имя` в
	// задании подключает скилл целиком ещё до первого хода. Кавычка — не '\'': правило
	// codex не признаёт слово с обратной косой чертой, и вызов ушёл бы в песочницу без записи.
	"codex": {
		skillsHow: "Each of these is attached by its mention below — its text is already with you, follow it from the start.",
		skillRef:  func(name string) string { return "$" + name },
		quote:     `'"'"'`,
	},
}

func (s *Stage) skillsBlock(d style) (string, error) {
	skills := s.settings.Skills
	if err := s.tk.Library.AssertPresent(append(slices.Clone(skills.Always), skills.OnDemand...)); err != nil {
		return "", err
	}
	// Скиллы onDemand в задании не перечисляются: драйверы сами видят
	// скиллы этапа с описаниями из frontmatter и берут подходящие к реквесту.
	if len(skills.Always) == 0 {
		return "", nil
	}
	refs := make([]string, len(skills.Always))
	for i, name := range skills.Always {
		refs[i] = d.skillRef(name)
	}
	return "## Your Method\n\n" + d.skillsHow + " They are your method,\n" +
		"they are not optional, and nothing below repeats what is in them.\n\n" + bullets(refs), nil
}

func (s *Stage) prompt(d style, journal *domain.State) (string, error) {
	t := s.template()
	p := s.tk.protocol
	skills, err := s.skillsBlock(d)
	if err != nil {
		return "", err
	}
	values := map[string]string{
		"PROTOCOL": p.protocol(s.settings.Prompt.Protocol),
		"HARNESS":  p.harness(d.quote),
		"COMMANDS": "The recording command is `" + harness.Name + "`; its commands and their formats are in `" + harness.Name + " --help`.",
		"SKILLS":   skills,
	}
	order := []string{"PROTOCOL", "HARNESS", "COMMANDS", "SKILLS"}
	if t.has("DIFF_ORIGIN") {
		values["DIFF_ORIGIN"] = p.diffOrigin()
		order = append(order, "DIFF_ORIGIN")
	}
	if t.has("PLAN") {
		values["PLAN"] = p.plan(s.settings.Prompt.Plan)
		order = append(order, "PLAN")
	}
	for k, v := range s.values(s.tk.Materials, journal) {
		if _, ok := values[k]; !ok {
			order = append(order, k)
		}
		values[k] = v
	}
	return t.render(values, order)
}

// sharesPack — общая часть пакета идёт этапу, чей шаблон берёт пакет: этапу
// без него она добавила бы чтение пакета на каждом ходу.
func (s *Stage) sharesPack() bool { return s.template().has("PACK") }

// Run — этап; assessed — глубина от оценщика, journal — свёртка предыдущих этапов.
func (s *Stage) Run(assessed int, journal *domain.State) (err error) {
	defer s.markResponding()
	if err := s.tk.Context.Err(); err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			if e, ok := p.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", p)
			}
		}
	}()
	m := s.tk.Materials
	if !s.applies(m, journal) {
		logx.Logf("этап «%s» не поднимается: %s", s.Name(), s.skipReason)
		return nil
	}
	briefPath, err := s.tk.Artifacts.SaveBrief(s.ID(), s.brief())
	if err != nil {
		return err
	}
	level := s.settings.Level
	if level == config.Assessed {
		level = assessed
	}
	settings := s.tk.Config.Levels[s.driver][level-1]
	prompt, err := s.prompt(styles[s.driver], journal)
	if err != nil {
		return err
	}
	promptPath := s.tk.Artifacts.SavePrompt(s.Name(), prompt)
	shared := ""
	if s.sharesPack() {
		shared = s.tk.Shared
	}
	logx.Logf("этап «%s» пошёл: %s, уровень %d (%s/%s), промпт %d символов → %s", s.Name(), s.driver, level, settings.Model, settings.Effort, utf8.RuneCountInString(prompt), promptPath)
	telemetry := agent.NewTelemetry(s.Name(), s.commands, s.settings.Skills.Always, m.Cwd, s.tk.Artifacts.OpenStream(s.Name()))
	go func() {
		<-telemetry.Responding()
		s.markResponding()
	}()
	outcome, err := s.tk.Drivers[s.driver].Run(s.tk.Context, drivers.Args{
		Name: s.ID(), Model: settings.Model, Effort: settings.Effort, ProjectInstructions: s.projectMemory, Allow: s.allow(),
		SystemPromptFile: shared, Prompt: prompt,
		Env: []string{harness.BriefEnv + "=" + briefPath}, Skills: append(slices.Clone(s.settings.Skills.Always), s.settings.Skills.OnDemand...),
		Cwd: m.Cwd, Timeout: 20 * time.Minute, SessionDir: s.tk.Artifacts.SessionDir(s.ID()),
	}, telemetry)
	if err != nil {
		telemetry.Close()
		return err
	}
	telemetry.Finish(outcome)
	s.record = s.judged(outcome)
	if !s.record.OK {
		return s.Journal().RecordFailure(s.ID(), s.settings.Key, s.record.Reason, outcome.RateLimit)
	}
	return nil
}

// judged — запись этапа. Процесс этапа мог выйти чисто, потеряв работу: вызов харнесса не
// дошёл до журнала или разбор не закрыт сводкой. Такой этап — сорванный, как упавший.
func (s *Stage) judged(o *agent.Outcome) *cost.Stage {
	r := &cost.Stage{Name: s.Name(), Driver: s.driver, OK: o.OK, Reason: o.Reason, Outcome: o}
	lost := lostWrites(o.Events)
	if lost > 0 {
		logx.Logf("[%s] вызовов %s, не дошедших до журнала: %d", s.Name(), harness.Name, lost)
	}
	if !r.OK {
		return r
	}
	state := s.State()
	reason := ""
	if lost > 0 {
		reason = fmt.Sprintf("вызовов харнесса не дошло до журнала: %d", lost)
	} else if slices.Contains(s.commands, "summary") && len(state.Summaries) == 0 && len(state.Stopped) == 0 {
		reason = "сводка не записана"
	}
	if reason == "" {
		return r
	}
	logx.Logf("этап «%s» сорван: %s", s.Name(), reason)
	r.OK, r.Reason = false, reason
	return r
}

// brief — задание харнесса. diffPath — изменения задачи: находка на чужой
// строке отбивается сразу. siblings — экземпляры этого же этапа на других
// драйверах: их находок этап не видит, увидев чужие, он не записал бы своих на
// тех же местах. Сводит экземпляры дедупликация.
func (s *Stage) brief() *harness.Brief {
	m := s.tk.Materials
	siblings := []string{}
	for _, d := range s.settings.Drivers {
		if d != s.driver {
			siblings = append(siblings, config.InstanceName(s.settings, d))
		}
	}
	b := &harness.Brief{Stage: s.ID(), Driver: s.driver, Name: s.Name(), Siblings: siblings, JournalDir: s.Journal().Dir,
		BaseSha: m.BaseSha(), DiffPath: m.DiffPath, Revs: m.Diff.Revs, RelatedRequired: s.relatedRequired, Commands: s.commands}
	if s.threads {
		b.Threads = m.OpenThreads()
	}
	return b
}

func withBase(commands ...string) []string { return append(commands, "diff", "stop") }

// NewAssess — оценщик: природа правки, риск и уровень, на котором пойдёт разбор.
func NewAssess(tk *Toolkit) *Stage {
	s := newStage(tk, tk.Config.Stage("assess"), "")
	s.name = "оценщик"
	s.commands = withBase("assess")
	s.values = func(m *materials.Materials, _ *domain.State) map[string]string {
		return map[string]string{"SIGNALS": m.Signals.Summary(), "FILES": bullets(m.Files()), "PACK": m.RenderPack(true)}
	}
	return s
}

// NewThreads — треды: сверяет наши открытые треды прошлых прогонов с текущим кодом.
func NewThreads(tk *Toolkit) *Stage {
	s := newStage(tk, tk.Config.Stage("threads"), "")
	s.name = "треды"
	s.commands = withBase("resolve", "thread", "list", "batch", "blame")
	s.threads = true
	s.applies = func(m *materials.Materials, _ *domain.State) bool { return len(m.OpenThreads()) > 0 }
	s.skipReason = "наших открытых тредов нет"
	s.values = func(m *materials.Materials, _ *domain.State) map[string]string {
		var items []string
		for _, t := range m.OpenThreads() {
			item := "- id " + t.ID + " — " + t.At + "\n  " + t.Text
			if t.Reply != nil {
				item += "\n  author's reply: " + *t.Reply
			}
			items = append(items, item)
		}
		return map[string]string{"THREADS": strings.Join(items, "\n"), "PACK": m.RenderPack(true)}
	}
	return s
}

// Чем встроенные разбирающие этапы отличаются друг от друга, кроме шаблона:
// вывод по реквесту (`summary`) пишет только бизнес-логика, память проекта
// нужна логике и качеству, текст задачи качеству кода не нужен. Свой этап
// конфига устроен как бизнес-логика.
type traits struct{ summary, projectMemory, withTask bool }

var analysisTraits = map[string]traits{
	"logic":    {true, true, true},
	"quality":  {false, true, false},
	"comments": {false, false, true},
	"tests":    {false, false, true},
}

// NewAnalysis — разбирающий этап, встроенный или свой из конфига. У встроенного
// свой шаблон, у своего — общий `analysis` с задачей из конфига.
func NewAnalysis(tk *Toolkit, settings *config.Stage, driver string) *Stage {
	s := newStage(tk, settings, driver)
	t, ok := analysisTraits[settings.Key]
	if !ok {
		t = analysisTraits["logic"]
	}
	if !config.IsBuiltin(settings.Key) {
		s.templateKey = "analysis"
	}
	s.projectMemory = t.projectMemory
	// Комментарий сам и есть место находки: о декларации этап скажет в --related, когда она есть.
	s.relatedRequired = settings.Key != "comments"
	s.commands = slices.Clone(findingCommands)
	if t.summary {
		s.commands = append(s.commands, "summary")
	}
	s.commands = withBase(s.commands...)
	s.applies = func(m *materials.Materials, _ *domain.State) bool { return len(m.FilesMatching(settings.Paths)) > 0 }
	s.skipReason = "в изменениях нет файлов под маски " + strings.Join(settings.Paths, ", ")
	s.values = func(m *materials.Materials, _ *domain.State) map[string]string {
		tmpl := s.template()
		v := map[string]string{}
		if tmpl.has("FILES") {
			v["FILES"] = bullets(m.FilesMatching(settings.Paths))
		}
		if tmpl.has("PACK") {
			v["PACK"] = m.RenderPack(t.withTask)
		}
		if tmpl.has("COMMENTS") {
			comments := materials.CommentsContext(m.Diff, m.Cwd)
			if comments == "" {
				comments = "(no added comments)"
			}
			v["COMMENTS"] = materials.Tagged("added_comments", comments)
		}
		if tmpl.has("TASK") {
			v["TASK"] = strings.TrimSpace(settings.Task)
		}
		if tmpl.has("TASK_NOTE") {
			v["TASK_NOTE"] = ""
			if m.Issue != "" {
				v["TASK_NOTE"] = "The task is in the pack below — check the implementation against what was asked for."
			}
		}
		return v
	}
	return s
}

// NewDedupe — дедупликация: помечает находки разных этапов про одно место и одну причину.
func NewDedupe(tk *Toolkit) *Stage {
	s := newStage(tk, tk.Config.Stage("dedupe"), "")
	s.name = "дедупликация"
	s.commands = withBase("dupe", "list", "batch", "blame")
	s.applies = func(_ *materials.Materials, j *domain.State) bool { return len(j.Findings) > 1 }
	s.skipReason = "находок меньше двух — дублировать нечему"
	s.values = func(_ *materials.Materials, j *domain.State) map[string]string {
		return map[string]string{"STAGES": FormatStages(tk.Config.AnalysisStages()), "FINDINGS": formatFindings(plain(j.Findings))}
	}
	return s
}

// NewSkeptic — скептик: вердикт по каждой уцелевшей находке, вес в обе стороны.
func NewSkeptic(tk *Toolkit) *Stage {
	s := newStage(tk, tk.Config.Stage("skeptic"), "")
	s.name = "скептик"
	s.projectMemory = true
	s.commands = withBase("verdict", "list", "blame", "batch")
	s.applies = func(_ *materials.Materials, j *domain.State) bool { return len(j.Kept()) > 0 }
	s.skipReason = "после дедупликации находок не осталось"
	s.values = func(m *materials.Materials, j *domain.State) map[string]string {
		return map[string]string{"STAGES": FormatStages(tk.Config.AnalysisStages()), "FINDINGS": formatFindings(findingContexts(j.Kept(), m.Cwd)),
			"PACK": m.RenderPack(true)}
	}
	return s
}
