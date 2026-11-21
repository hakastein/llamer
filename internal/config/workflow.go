package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/skills"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// WorkflowFile — конфиг workflow в его каталоге.
const WorkflowFile = "workflow.json"

//go:embed workflow.schema.json
var workflowSchemaText []byte

var workflowSchema = mustCompile("workflow.schema.json", workflowSchemaText)

// PromptData — то, что шаблон этапа получает при сборке задания: ответы
// прошедших этапов по имени — JSON у этапа со схемой, текст у этапа без неё.
type PromptData struct{ Answers map[string]string }

// WorkflowStage — этап workflow: запуск агента или, с Command, команда workflow без агента.
type WorkflowStage struct {
	Name, Driver  string
	Model, Effort string
	Template      *template.Template
	Skills        []string
	Timeout       time.Duration
	SchemaPath    string             // пусто — ответ свободным текстом
	Schema        *jsonschema.Schema // nil — ответ свободным текстом
	// Command — argv команды из `commands/` с раскрытыми `${VAR}`; nil — этап агента.
	Command []string
	// Allow — команды из `commands/`, которые этап может звать: у этапа агента — из workflow, у этапа-команды —
	// его команда.
	Allow []string
}

// Workflow — проверенный конфиг workflow: волны этапов в порядке файла и скиллы из `skills/` его каталога.
type Workflow struct {
	Dir     string
	Library *skills.Library
	// Waves — этапы по волнам: волна идёт после предыдущей, её этапы — одновременно.
	Waves [][]*WorkflowStage
	// Readable — пути сверх каталога контекста, которые агент читает в песочнице: абсолютные.
	Readable []string
	// Commands — команды агента из `commands/` каталога workflow; nil — каталога нет.
	Commands *cliche.Catalog
	// Hosts и Programs — куда командам можно ходить по HTTP и какие программы запускать.
	Hosts, Programs []string
}

type (
	rawWorkflow struct {
		Readable []string
		Commands struct{ Hosts, Programs []string }
		Stages   []rawWave
	}
	rawWorkflowStage struct {
		Name, Driver, Model, Effort, Prompt, Timeout, Schema string
		Skills, Command, Allow                               []string
	}
	// rawWave — элемент stages: массив этапов или один этап — волна из него.
	rawWave struct {
		stages []rawWorkflowStage
		single bool
	}
)

func (w *rawWave) UnmarshalJSON(data []byte) error {
	var stage rawWorkflowStage
	// Схема уже проверена: что не разбирается этапом, — массив этапов.
	if json.Unmarshal(data, &stage) == nil {
		w.stages, w.single = []rawWorkflowStage{stage}, true
		return nil
	}
	return json.Unmarshal(data, &w.stages)
}

// LoadWorkflow — workflow из каталога dir (путь абсолютный); `${VAR}` в argv этапов-команд раскрываются из env.
// Шаблон этапа собирается на ответах прошлых волн: ссылка на этап своей или следующей волны или опечатка в
// имени останавливают прогон до первого запроса к модели.
func LoadWorkflow(dir string, env map[string]string) (w *Workflow, err error) {
	defer caught(&err)
	file := filepath.Join(dir, WorkflowFile)
	data, err := readJSON(file)
	if err != nil {
		return nil, err
	}
	r := reader{file}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := r.validate(workflowSchema, doc); err != nil {
		return nil, err
	}
	var raw rawWorkflow
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	library, err := skills.LoadLibrary(os.DirFS(dir), nil)
	if err != nil {
		return nil, err
	}
	w = &Workflow{Dir: dir, Library: library, Hosts: raw.Commands.Hosts, Programs: raw.Commands.Programs}
	if w.Commands, err = commands(filepath.Join(dir, CommandsDir)); err != nil {
		r.fail(CommandsDir, err.Error())
	}
	for i, path := range raw.Readable {
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err != nil {
			r.fail(fmt.Sprintf("readable[%d]", i), err.Error())
		}
		w.Readable = append(w.Readable, path)
	}
	answers := map[string]string{}
	for i, wave := range raw.Stages {
		path := fmt.Sprintf("stages[%d]", i)
		stages := make([]*WorkflowStage, len(wave.stages))
		names := map[string]bool{}
		for j, s := range wave.stages {
			at := path
			if !wave.single {
				at = fmt.Sprintf("%s[%d]", path, j)
			}
			if _, ok := answers[s.Name]; ok || names[s.Name] {
				r.fail(at+".name", "этап "+s.Name+" уже есть")
			}
			names[s.Name] = true
			stages[j] = r.workflowStage(w, s, at, answers, env)
		}
		// Ответы волны — только следующим волнам: её этапы идут одновременно.
		for name := range names {
			answers[name] = ""
		}
		w.Waves = append(w.Waves, stages)
	}
	return w, nil
}

// workflowStage — этап s по пути path в файле; answers — этапы прошлых волн.
func (r reader) workflowStage(w *Workflow, s rawWorkflowStage, path string, answers, env map[string]string) *WorkflowStage {
	if s.Command != nil {
		allow := []string{r.command(w, s.Command, path+".command")}
		argv := make([]string, len(s.Command))
		for i, arg := range s.Command {
			argv[i] = r.expandVars(arg, env, fmt.Sprintf("%s.command[%d]", path, i))
		}
		return &WorkflowStage{Name: s.Name, Command: argv, Allow: allow}
	}
	for i, c := range s.Allow {
		r.command(w, strings.Fields(c), fmt.Sprintf("%s.allow[%d]", path, i))
	}
	text, err := os.ReadFile(filepath.Join(w.Dir, s.Prompt))
	if err != nil {
		r.fail(path+".prompt", err.Error())
	}
	tmpl, err := template.New(s.Prompt).Option("missingkey=error").Parse(string(text))
	if err == nil {
		err = tmpl.Execute(io.Discard, PromptData{Answers: answers})
	}
	if err != nil {
		r.fail(path+".prompt", err.Error())
	}
	timeout, err := time.ParseDuration(s.Timeout)
	if err != nil {
		r.fail(path+".timeout", err.Error())
	}
	if timeout <= 0 {
		r.fail(path+".timeout", "должен быть больше нуля")
	}
	if missing := w.Library.Missing(s.Skills); len(missing) > 0 {
		r.fail(path+".skills", fmt.Sprintf("в %s нет скиллов %v", filepath.Join(w.Dir, "skills"), missing))
	}
	stage := &WorkflowStage{Name: s.Name, Driver: s.Driver, Model: s.Model, Effort: s.Effort, Template: tmpl,
		Skills: s.Skills, Timeout: timeout, Allow: s.Allow}
	if s.Schema != "" {
		stage.SchemaPath = filepath.Join(w.Dir, s.Schema)
		if stage.Schema, err = jsonschema.NewCompiler().Compile(stage.SchemaPath); err != nil {
			r.fail(path+".schema", err.Error())
		}
	}
	return stage
}

// command — команда из `commands/` каталога workflow, которой начинается argv; нет такой — ошибка по пути path.
func (r reader) command(w *Workflow, argv []string, path string) string {
	i := -1
	if w.Commands != nil {
		i = slices.IndexFunc(w.Commands.Commands, func(c *cliche.Command) bool {
			return len(c.Path) <= len(argv) && slices.Equal(c.Path, argv[:len(c.Path)])
		})
	}
	if i < 0 {
		r.fail(path, "в "+CommandsDir+"/ каталога workflow нет команды "+strings.Join(argv, " "))
	}
	return strings.Join(w.Commands.Commands[i].Path, " ")
}

// CommandsDir — каталог cliche-скриптов команд агента в каталоге workflow.
const CommandsDir = "commands"

// commands — команды агента из каталога dir; nil — каталога нет. Сломанный скрипт останавливает прогон
// до первого запроса к модели.
func commands(dir string) (*cliche.Catalog, error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	roots := cliche.Config{Sources: []cliche.Source{cliche.Dir(dir)}}.Scan()
	if roots[0].Err != nil {
		return nil, roots[0].Err
	}
	catalog := cliche.Merge(roots)
	var broken []error
	for _, b := range catalog.Broken {
		broken = append(broken, b.Err)
	}
	return catalog, errors.Join(broken...)
}

// Drivers — драйверы этапов агента.
func (w *Workflow) Drivers() []string {
	var out []string
	for _, s := range slices.Concat(w.Waves...) {
		if s.Command == nil && !slices.Contains(out, s.Driver) {
			out = append(out, s.Driver)
		}
	}
	return out
}

// Skills — скиллы всех этапов workflow.
func (w *Workflow) Skills() []string {
	var out []string
	for _, s := range slices.Concat(w.Waves...) {
		for _, name := range s.Skills {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}
