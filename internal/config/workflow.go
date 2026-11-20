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

// WorkflowStage — этап workflow.
type WorkflowStage struct {
	Name, Driver  string
	Model, Effort string
	Template      *template.Template
	Skills        []string
	Timeout       time.Duration
	SchemaPath    string             // пусто — ответ свободным текстом
	Schema        *jsonschema.Schema // nil — ответ свободным текстом
}

// Workflow — проверенный конфиг workflow: этапы в порядке файла и скиллы из `skills/` его каталога.
type Workflow struct {
	Dir     string
	Library *skills.Library
	Stages  []*WorkflowStage
	// Readable — пути сверх каталога контекста, которые агент читает в песочнице: абсолютные.
	Readable []string
	// Commands — команды агента из `commands/` каталога workflow; nil — каталога нет.
	Commands *cliche.Catalog
	// Hosts и Programs — куда командам можно ходить по HTTP и какие программы запускать.
	Hosts, Programs []string
}

type rawWorkflow struct {
	Readable []string
	Commands struct{ Hosts, Programs []string }
	Stages   []struct {
		Name, Driver, Model, Effort, Prompt, Timeout, Schema string
		Skills                                               []string
	}
}

// LoadWorkflow — workflow из каталога dir (путь абсолютный). Шаблон этапа
// собирается на ответах этапов до него: ссылка на этап позже или опечатка в
// имени останавливают прогон до первого запроса к модели.
func LoadWorkflow(dir string) (w *Workflow, err error) {
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
	for i, s := range raw.Stages {
		path := fmt.Sprintf("stages[%d]", i)
		if _, ok := answers[s.Name]; ok {
			r.fail(path+".name", "этап "+s.Name+" уже есть")
		}
		text, err := os.ReadFile(filepath.Join(dir, s.Prompt))
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
		if missing := library.Missing(s.Skills); len(missing) > 0 {
			r.fail(path+".skills", fmt.Sprintf("в %s нет скиллов %v", filepath.Join(dir, "skills"), missing))
		}
		stage := &WorkflowStage{Name: s.Name, Driver: s.Driver, Model: s.Model, Effort: s.Effort, Template: tmpl,
			Skills: s.Skills, Timeout: timeout}
		if s.Schema != "" {
			stage.SchemaPath = filepath.Join(dir, s.Schema)
			if stage.Schema, err = jsonschema.NewCompiler().Compile(stage.SchemaPath); err != nil {
				r.fail(path+".schema", err.Error())
			}
		}
		w.Stages = append(w.Stages, stage)
		answers[s.Name] = ""
	}
	return w, nil
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

// Drivers — драйверы этапов workflow.
func (w *Workflow) Drivers() []string {
	var out []string
	for _, s := range w.Stages {
		if !slices.Contains(out, s.Driver) {
			out = append(out, s.Driver)
		}
	}
	return out
}

// Skills — скиллы всех этапов workflow.
func (w *Workflow) Skills() []string {
	var out []string
	for _, s := range w.Stages {
		for _, name := range s.Skills {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}
