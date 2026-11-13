// Package config — конфиг проекта `.llamer.json` v2 и личный `.llamer.local.json`.
package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

const (
	File = ".llamer.json"
	// LocalFile — личный конфиг рядом с конфигом проекта: накладывается на него, в репозиторий не идёт.
	LocalFile = ".llamer.local.json"
	// LevelCount — уровней глубины пять: оценщик выбирает номер, этап идёт моделью этого уровня на лестнице своего драйвера.
	LevelCount = 5
	// Assessed — уровень этапа «глубина от оценщика».
	Assessed = 0
)

// ServiceStages — служебные этапы: идут одним экземпляром.
var ServiceStages = []string{"assess", "dedupe", "skeptic", "threads"}

type builtin struct {
	key    string
	skills []string
	level  int
	title  string
	paths  []string
}

// Встроенные этапы идут всегда. Задание каждого — в его шаблоне; `title`
// встроенного разбирающего этапа — то, как его называют дедупликация и скептик.
// Скиллы — встроенные в бинарник; методу тестов и комментариев своего скилла
// нет, его дают конфигом. `paths` — маски по умолчанию.
var builtins = []builtin{
	{key: "assess", skills: []string{"assessing-review-scope"}, level: 1},
	{key: "threads", level: 1},
	{key: "logic", skills: []string{"reviewing-business-logic"}, level: Assessed, title: "the business-logic reviewer"},
	{key: "quality", skills: []string{"reviewing-code-quality"}, level: 2, title: "the code-quality reviewer"},
	{key: "comments", level: 1, title: "the comments reviewer"},
	{key: "tests", level: 1, title: "the test reviewer", paths: []string{"**/tests/**"}},
	{key: "dedupe", level: 1},
	{key: "skeptic", skills: []string{"refuting-review-findings"}, level: Assessed},
}

func builtinOf(key string) *builtin {
	for i := range builtins {
		if builtins[i].key == key {
			return &builtins[i]
		}
	}
	return nil
}

// IsBuiltin — этап встроенный.
func IsBuiltin(key string) bool { return builtinOf(key) != nil }

// Level — модель и усилие одного уровня лестницы драйвера.
type Level struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// Driver — настройки драйвера.
type Driver struct {
	Bin    string
	Env    [][2]string
	Levels []Level
}

// Drivers — встроенные драйверы и их умолчания.
var Drivers = []string{"claude", "codex"}

// Defaults — умолчания драйверов: лестница своя у каждого, номер уровня один.
var Defaults = map[string]Driver{
	"claude": {Bin: "claude", Levels: []Level{
		{"claude-sonnet-5-5", "medium"},
		{"claude-sonnet-5-5", "high"},
		{"claude-opus-5-5", "low"},
		{"claude-opus-5-5", "medium"},
		{"claude-opus-5-5", "high"},
	}},
	"codex": {Bin: "codex", Levels: []Level{
		{"gpt-6.1-sol", "medium"},
		{"gpt-6.1-sol", "high"},
		{"gpt-6.1-sol", "xhigh"},
		{"gpt-6.1-sol", "max"},
		{"gpt-6.1-sol", "ultra"},
	}},
}

// Skills — скиллы этапа: always — метод, onDemand — по выбору агента.
type Skills struct{ Always, OnDemand []string }

// Prompt — строки задания, которые знает проект, а не движок; пустая строка — не задано.
type Prompt struct{ Protocol, Plan string }

// Stage — этап конфига.
type Stage struct {
	Key     string
	Title   string // у служебных этапов пусто
	Task    string // у встроенных пусто
	Drivers []string
	Skills  Skills
	Paths   []string
	Level   int // номер или Assessed
	Allow   []string
	Prompt  Prompt
}

// Config — проверенный конфиг прогона.
type Config struct {
	Source     string
	SkillPaths []string
	Allow      []string
	Drivers    map[string]Driver
	Stages     []*Stage // встроенные первыми, свои — в порядке конфига
}

// Stage — этап по ключу.
func (c *Config) Stage(key string) *Stage {
	for _, s := range c.Stages {
		if s.Key == key {
			return s
		}
	}
	return nil
}

// Error — конфиг не годится; текст читает человек.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Строение конфига — в схеме: опечатка в ключе иначе молча выключала бы то,
// что человек думал включить. Команда `allow` — слова без метасимволов
// оболочки: разрешение префиксное, и `;` или `|` в нём разрешили бы цепочку чего угодно.
//
//go:embed schema.json
var schemaText []byte

var schema = func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaText))
	if err != nil {
		panic(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("schema.json")
}()

// Проверенный схемой конфиг: nil — ключа нет.
type (
	rawConfig struct {
		SkillPaths []string              `json:"skillPaths"`
		Allow      []string              `json:"allow"`
		Prompt     *rawPrompt            `json:"prompt"`
		Driver     string                `json:"driver"`
		Drivers    map[string]*rawDriver `json:"drivers"`
		Stages     map[string]*rawStage  `json:"stages"`
	}
	rawPrompt struct {
		Protocol *string `json:"protocol"`
		Plan     *string `json:"plan"`
	}
	rawDriver struct {
		Bin    string            `json:"bin"`
		Env    map[string]string `json:"env"`
		Levels []Level           `json:"levels"`
	}
	rawStage struct {
		Task    string     `json:"task"`
		Drivers []string   `json:"drivers"`
		Skills  *rawSkills `json:"skills"`
		Level   any        `json:"level"` // «assessed» или номер
		Paths   *[]string  `json:"paths"`
		Allow   []string   `json:"allow"`
		Prompt  *rawPrompt `json:"prompt"`
	}
	rawSkills struct {
		Always   []string `json:"always"`
		OnDemand []string `json:"onDemand"`
	}
)

type reader struct{ file string }

func (r reader) fail(path, message string) {
	if path != "" {
		path += ": "
	}
	panic(&Error{r.file + ": " + path + message})
}

// `${VAR}` и `~` раскрываются: каталог скиллов у CI и у разработчика разный,
// а конфиг один на репозиторий. `${VAR}` — и в значениях drivers.<имя>.env.
var varRef = regexp.MustCompile(`\$\{(\w+)\}`)

func (r reader) expandVars(raw string, env map[string]string, path string) string {
	return varRef.ReplaceAllStringFunc(raw, func(m string) string {
		name := varRef.FindStringSubmatch(m)[1]
		value, ok := env[name]
		if !ok {
			r.fail(path, "переменная "+name+" не задана")
		}
		return value
	})
}

var tilde = regexp.MustCompile(`^~(/|$)`)

func (r reader) expandPath(raw string, env map[string]string, base, path string) string {
	expanded := r.expandVars(raw, env, path)
	if tilde.MatchString(expanded) {
		home, err := os.UserHomeDir()
		if err != nil {
			r.fail(path, err.Error())
		}
		expanded = home + expanded[1:]
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	return filepath.Join(base, expanded)
}

func (r reader) driver(name string, raw *rawDriver, env map[string]string) Driver {
	d := Defaults[name]
	if raw == nil {
		return d
	}
	for _, key := range slices.Sorted(maps.Keys(raw.Env)) {
		text := raw.Env[key]
		// Пробрасываем, только если есть: DOCKER_HOST и подобные заданы в CI, а локально нет.
		missing := false
		for _, m := range varRef.FindAllStringSubmatch(text, -1) {
			if _, ok := env[m[1]]; !ok {
				missing = true
			}
		}
		if !missing {
			d.Env = append(slices.Clone(d.Env), [2]string{key, r.expandVars(text, env, "drivers."+name+".env."+key)})
		}
	}
	if raw.Bin != "" {
		d.Bin = raw.Bin
	}
	if raw.Levels != nil {
		d.Levels = raw.Levels
	}
	return d
}

// prompt — строки задания, которые знает проект, а не движок: `protocol`
// дописывается в «How This Run Works», `plan` заменяет план по умолчанию
// (поиск по тексту). Так проект говорит этапу о навигации, которую сам поднял
// и разрешил в `allow`. `prompt` этапа перекрывает общий по ключам.
func prompt(raw *rawPrompt, base Prompt) Prompt {
	if raw != nil && raw.Protocol != nil {
		base.Protocol = *raw.Protocol
	}
	if raw != nil && raw.Plan != nil {
		base.Plan = *raw.Plan
	}
	return base
}

// Этап конфига поверх встроенного: встроенному добавляются скиллы, задаются
// уровень, `allow` и `prompt`, разбирающему — ещё маски; свой этап несёт задачу
// и маски путей. Встроенные скиллы этапа идут в always: убрать их конфигом нельзя.
// stageName — имя своего этапа. Не в схеме: jsonschema v6.0.3 ставит ошибке
// propertyNames место последнего проверенного ключа корня, а не `stages`.
var stageName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func (r reader) stage(key string, raw *rawStage, base Prompt, driver string) *Stage {
	path := "stages." + key
	if !stageName.MatchString(key) {
		r.fail(path, "имя этапа не подходит под "+stageName.String())
	}
	if raw == nil {
		raw = &rawStage{}
	}
	s := &Stage{Key: key, Title: "the " + key + " stage", Task: raw.Task, Drivers: raw.Drivers, Paths: []string{},
		Level: Assessed, Allow: raw.Allow, Prompt: prompt(raw.Prompt, base)}
	if s.Drivers == nil {
		s.Drivers = []string{driver}
	}
	if slices.Contains(ServiceStages, key) && len(s.Drivers) > 1 {
		r.fail(path+".drivers", key+" — служебный этап, он идёт одним экземпляром: ровно один драйвер")
	}
	if s.Allow == nil {
		s.Allow = []string{}
	}
	var skills []string
	if b := builtinOf(key); b != nil {
		s.Title, s.Level, skills = b.title, b.level, b.skills
		if b.paths != nil {
			s.Paths = b.paths
		}
	}
	switch level := raw.Level.(type) {
	case float64:
		s.Level = int(level)
	case string:
		s.Level = Assessed
	}
	if raw.Paths != nil {
		s.Paths = *raw.Paths
	}
	for i, mask := range s.Paths {
		if !doublestar.ValidatePattern(strings.TrimPrefix(mask, "!")) {
			r.fail(fmt.Sprintf("%s.paths[%d]", path, i), "маска «"+mask+"» не разбирается")
		}
	}
	var own rawSkills
	if raw.Skills != nil {
		own = *raw.Skills
	}
	s.Skills = Skills{Always: []string{}, OnDemand: own.OnDemand}
	for _, name := range append(skills, own.Always...) {
		if !slices.Contains(s.Skills.Always, name) {
			s.Skills.Always = append(s.Skills.Always, name)
		}
	}
	var both []string
	for _, name := range s.Skills.Always {
		if slices.Contains(s.Skills.OnDemand, name) {
			both = append(both, name)
		}
	}
	if len(both) > 0 {
		r.fail(path+".skills", "скилл в обоих списках, always и onDemand: "+strings.Join(both, ", "))
	}
	return s
}

// typeNames — типы JSON так, как их называет текст ошибки.
var typeNames = map[string]string{"object": "объектом", "array": "списком", "string": "строкой", "integer": "целым числом",
	"number": "числом", "boolean": "true или false", "null": "null"}

// schemaError — нарушения схемы через «; »: место в записи `stages.docs.drivers[0]` и что не так.
// Тексты библиотеки английские и говорят на языке схемы, а читает их тот, кто правит конфиг.
func schemaError(e *jsonschema.ValidationError) []string {
	var at strings.Builder
	for _, seg := range e.InstanceLocation {
		// shortcut: цифровой ключ объекта тоже станет [n] — таких ключей в схеме нет.
		if _, err := strconv.Atoi(seg); err == nil {
			at.WriteString("[" + seg + "]")
		} else {
			if at.Len() > 0 {
				at.WriteString(".")
			}
			at.WriteString(seg)
		}
	}
	path := at.String()
	if path != "" {
		path += ": "
	}
	var message string
	switch k := e.ErrorKind.(type) {
	case *kind.AdditionalProperties:
		message = "неизвестные ключи: " + strings.Join(k.Properties, ", ")
	case *kind.Required:
		message = "нет ключей: " + strings.Join(k.Missing, ", ")
	case *kind.Type:
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = typeNames[w]
		}
		message = "должен быть " + strings.Join(want, " или ")
	case *kind.Const:
		message = fmt.Sprintf("поддерживается %v", k.Want)
	case *kind.Enum:
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = fmt.Sprint(w)
		}
		message = "одно из " + strings.Join(want, ", ")
	case *kind.MinItems:
		message = fmt.Sprintf("в списке не меньше %d", k.Want)
	case *kind.MaxItems:
		message = fmt.Sprintf("в списке не больше %d", k.Want)
	case *kind.UniqueItems:
		message = fmt.Sprintf("элементы %d и %d повторяются", k.Duplicates[0], k.Duplicates[1])
	case *kind.Pattern:
		message = "«" + k.Got + "» не подходит"
		if k.Want == `\S` {
			message = "должен быть непустой строкой"
		}
	default:
		var out []string
		for _, c := range e.Causes {
			out = append(out, schemaError(c)...)
		}
		if len(out) > 0 {
			return out
		}
		message = e.Error()
	}
	return []string{path + message}
}

// Parse — конфиг проекта. Каждый ключ проверяется до прогона: незнакомое
// останавливает его, а не выключает молча то, что человек думал включить.
// order — ключи `stages` в порядке файла: свои этапы идут в нём.
func Parse(data []byte, order []string, file string, env map[string]string, source string) (config *Config, err error) {
	defer func() {
		if p := recover(); p != nil {
			e, ok := p.(*Error)
			if !ok {
				panic(p)
			}
			err = e
		}
	}()
	r := reader{source}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, &Error{fmt.Sprintf("%s: не читается как JSON (%s)", source, err)}
	}
	if err := schema.Validate(doc); err != nil {
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) {
			return nil, err
		}
		r.fail("", strings.Join(schemaError(invalid), "; "))
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	drivers := map[string]Driver{}
	for _, name := range Drivers {
		drivers[name] = r.driver(name, raw.Drivers[name], env)
	}
	base := prompt(raw.Prompt, Prompt{})
	var keys []string
	for _, b := range builtins {
		keys = append(keys, b.key)
	}
	// Ключи, которых нет в order, — только у объекта не из файла; они идут по алфавиту.
	for _, k := range append(order, slices.Sorted(maps.Keys(raw.Stages))...) {
		if _, ok := raw.Stages[k]; ok && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	var stages []*Stage
	for _, key := range keys {
		stages = append(stages, r.stage(key, raw.Stages[key], base, raw.Driver))
	}
	skillPaths := make([]string, len(raw.SkillPaths))
	for i, p := range raw.SkillPaths {
		skillPaths[i] = r.expandPath(p, env, filepath.Dir(file), fmt.Sprintf("skillPaths[%d]", i))
	}
	if raw.Allow == nil {
		raw.Allow = []string{}
	}
	return &Config{Source: source, SkillPaths: skillPaths, Allow: raw.Allow, Drivers: drivers, Stages: stages}, nil
}

// stageOrder — ключи объекта `stages` в порядке текста: карта JSON его теряет.
func stageOrder(data []byte) []string {
	var root struct {
		Stages json.RawMessage `json:"stages"`
	}
	if json.Unmarshal(data, &root) != nil || len(root.Stages) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(root.Stages))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

func readJSON(file string) ([]byte, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, &Error{fmt.Sprintf("%s: не читается (%s)", file, err)}
	}
	if !json.Valid(data) {
		var v any
		return nil, &Error{fmt.Sprintf("%s: не читается как JSON (%s)", file, json.Unmarshal(data, &v))}
	}
	return data, nil
}

// Load — конфиг проекта с наложенным `.llamer.local.json` (JSON Merge Patch,
// RFC 7396) из того же каталога; проверяется итог.
func Load(file string, env map[string]string) (*Config, error) {
	if _, err := os.Stat(file); err != nil {
		return nil, &Error{"конфиг " + file + " не найден — положи " + File + " в корень проекта или передай --config"}
	}
	local := filepath.Join(filepath.Dir(file), LocalFile)
	data, err := readJSON(file)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(local); err != nil {
		return Parse(data, stageOrder(data), file, env, file)
	}
	patch, err := readJSON(local)
	if err != nil {
		return nil, err
	}
	merged, err := jsonpatch.MergePatch(data, patch)
	if err != nil {
		return nil, &Error{fmt.Sprintf("%s + %s: не накладывается (%s)", file, local, err)}
	}
	return Parse(merged, append(stageOrder(data), stageOrder(patch)...), file, env, file+" + "+local)
}

// AnalysisStages — разбирающие этапы: все, кроме служебных, встроенные первыми.
func (c *Config) AnalysisStages() []*Stage {
	var out []*Stage
	for _, s := range c.Stages {
		if !slices.Contains(ServiceStages, s.Key) {
			out = append(out, s)
		}
	}
	return out
}

// DriversInUse — драйверы всех этапов конфига.
func (c *Config) DriversInUse() []string {
	var out []string
	for _, s := range c.Stages {
		for _, d := range s.Drivers {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// SkillsInUse — скиллы всех этапов конфига, обоих списков.
func (c *Config) SkillsInUse() []string {
	var out []string
	for _, s := range c.Stages {
		for _, name := range append(slices.Clone(s.Skills.Always), s.Skills.OnDemand...) {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// InstanceName — имя экземпляра этапа: этап на нескольких драйверах идёт экземпляром на каждый, `logic·codex`.
func InstanceName(stage *Stage, driver string) string {
	if len(stage.Drivers) > 1 {
		return stage.Key + "·" + driver
	}
	return stage.Key
}
