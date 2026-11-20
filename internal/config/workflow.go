package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"text/template"
	"time"

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
	Name, Driver string
	Level        Level
	Template     *template.Template
	Skills       []string
	Timeout      time.Duration
	SchemaPath   string             // пусто — ответ свободным текстом
	Schema       *jsonschema.Schema // nil — ответ свободным текстом
}

// Workflow — проверенный конфиг workflow: этапы в порядке файла и скиллы из `skills/` его каталога.
type Workflow struct {
	Dir     string
	Library *skills.Library
	Stages  []*WorkflowStage
}

type rawWorkflow struct {
	Stages []struct {
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
	w = &Workflow{Dir: dir, Library: library}
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
		stage := &WorkflowStage{Name: s.Name, Driver: s.Driver, Level: Level{Model: s.Model, Effort: s.Effort}, Template: tmpl,
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
