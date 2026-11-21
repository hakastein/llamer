package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// stage — этап агента по проверенному спеку.
type stage struct {
	Name, Driver, Model, Effort string
	Prompt                      string // текст задания
	Skills                      []string
	Timeout                     time.Duration
	SchemaPath                  string             // пусто — ответ свободным текстом
	Schema                      *jsonschema.Schema // nil — ответ свободным текстом
	Allow                       []string           // префиксы команд агенту: команды `commands/` и любые другие
	Readable                    []string           // пути сверх каталога контекста, которые агент читает: абсолютные
	Hosts, Programs             []string           // куда командам этапа можно ходить по HTTP и какие программы запускать
	Data                        any                // значение JSON, которое команды этапа читают как llamer.data
}

// spec — спек этапа, как его пишет скрипт.
type spec struct {
	Name, Driver, Model, Effort, Prompt, Timeout, Schema string
	Skills, Allow, Readable, Fetch, Exec                 []string
	Data                                                 any
}

var stageName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// module — модуль `llamer` скрипта workflow.
func (r *Run) module() *cliche.Object {
	return cliche.NewObject([]string{"agent", "prompt", "records", "env", "fetch", "exec"}, []any{
		cliche.Func(r.agent), cliche.Func(r.prompt), cliche.Func(r.commands.journal.records), cliche.Func(env),
		// Скрипт workflow — код проекта, а не ввод агента: его HTTP и программы не ограничены.
		cliche.Func(func(ctx context.Context, args ...any) (any, error) {
			return fetch(ctx, args, func(*url.URL) error { return nil })
		}),
		cliche.Func(func(ctx context.Context, args ...any) (any, error) {
			return run(ctx, r.Cwd, args, func(string) error { return nil })
		}),
	})
}

// agent(spec) → {started, result}: этап идёт в своей горутине, started выполняется с началом ответа модели (или с концом
// этапа, который так и не ответил), result — ответом этапа. Ошибка в спеке — исключение при вызове, срыв этапа
// отклоняет result; то и другое делает прогон неуспешным, даже если скрипт ошибку поймает, и следующие agent()
// отказывают. Отмена ctx — конец прогона — прерывает этап.
func (r *Run) agent(ctx context.Context, args ...any) (any, error) {
	if len(args) != 1 {
		err := errors.New("agent(spec) — ровно один спек")
		r.fail(&Failure{Reason: "spec", Detail: err.Error()})
		return nil, err
	}
	s, err := r.spec(args[0])
	if err != nil {
		r.fail(&Failure{Stage: s.Name, Reason: "spec", Detail: err.Error()})
		return nil, err
	}
	if f := r.failure(); f != nil {
		return nil, fmt.Errorf("этап %s не запущен: прогон уже сорван (%s %s)", s.Name, f.Stage, f.Reason)
	}
	started, result := new(cliche.Promise), new(cliche.Promise)
	r.stages.Go(func() {
		answer, err := r.launch(ctx, s, func() { started.Settle(cliche.Undefined{}, nil) })
		started.Settle(cliche.Undefined{}, nil)
		if err != nil {
			result.Settle(nil, err)
			return
		}
		result.Settle(bridged(answer), nil)
	})
	return cliche.NewObject([]string{"started", "result"}, []any{started, result}), nil
}

// spec — этап по спеку value; stage с именем — и при ошибке. Имя этапа занимается и неудачным спеком.
func (r *Run) spec(value any) (*stage, error) {
	plain, err := plainOf(value)
	object, isObject := plain.(map[string]any)
	if err != nil || !isObject {
		return &stage{}, errors.New("спек этапа — объект")
	}
	named := &stage{}
	named.Name, _ = object["name"].(string)
	data, err := json.Marshal(plain)
	if err != nil {
		return named, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var sp spec
	if err := dec.Decode(&sp); err != nil {
		return named, fmt.Errorf("спек этапа %s: %w", named.Name, err)
	}
	s := &stage{Name: sp.Name, Driver: sp.Driver, Model: sp.Model, Effort: sp.Effort, Prompt: sp.Prompt, Skills: sp.Skills,
		Allow: sp.Allow, Hosts: sp.Fetch, Programs: sp.Exec, Data: sp.Data}
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !stageName.MatchString(s.Name) {
		fail("name %q: нужны [a-z][a-z0-9_]*", s.Name)
	}
	r.mu.Lock()
	if r.names[s.Name] {
		fail("этап %s уже был", s.Name)
	}
	r.names[s.Name] = true
	r.mu.Unlock()
	if !slices.Contains(drivers.Names(), s.Driver) {
		fail("driver %q: нужен один из %q", s.Driver, drivers.Names())
	}
	for _, key := range [][2]string{{"model", s.Model}, {"effort", s.Effort}, {"prompt", s.Prompt}} {
		if strings.TrimSpace(key[1]) == "" {
			fail("нет %s", key[0])
		}
	}
	if s.Timeout, err = time.ParseDuration(sp.Timeout); err != nil || s.Timeout <= 0 {
		fail("timeout %q: нужна длительность больше нуля (10m)", sp.Timeout)
	}
	if missing := r.Project.Library.Missing(s.Skills); len(missing) > 0 {
		fail("skills: в %s нет %q", filepath.Join(r.Project.Dir, "skills"), missing)
	}
	if sp.Schema != "" {
		s.SchemaPath = filepath.Join(r.Project.Dir, sp.Schema)
		if s.Schema, err = jsonschema.NewCompiler().Compile(s.SchemaPath); err != nil {
			fail("schema: %v", err)
		}
	}
	if err := config.CheckAllow(s.Allow); err != nil {
		fail("allow: %v", err)
	}
	for _, path := range sp.Readable {
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.Project.Dir, path)
		}
		if _, err := os.Stat(path); err != nil {
			fail("readable: %v", err)
		}
		s.Readable = append(s.Readable, path)
	}
	if err := errors.Join(errs...); err != nil {
		return s, fmt.Errorf("спек этапа %s: %w", s.Name, err)
	}
	return s, nil
}

// prompt(file, data) → текст файла из каталога llamer как text/template на data; ключа нет в data — ошибка.
func (r *Run) prompt(_ context.Context, args ...any) (any, error) {
	file, ok := firstString(args)
	if !ok {
		return nil, errors.New("prompt(file, data) — файл строкой")
	}
	text, err := os.ReadFile(filepath.Join(r.Project.Dir, file))
	if err != nil {
		return nil, err
	}
	var data any
	if len(args) > 1 {
		if data, err = plainOf(args[1]); err != nil {
			return nil, err
		}
	}
	tmpl, err := template.New(file).Option("missingkey=error").Parse(string(text))
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, err
	}
	return out.String(), nil
}

// env(name) → значение переменной окружения llamer или undefined: так в workflow приходят токены, не попадая в argv.
func env(_ context.Context, args ...any) (any, error) {
	name, ok := firstString(args)
	if !ok {
		return nil, errors.New("env(name) — имя строкой")
	}
	if value, ok := os.LookupEnv(name); ok {
		return value, nil
	}
	return cliche.Undefined{}, nil
}
