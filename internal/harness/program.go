// Package harness — команда `review`, которой этап пишет журнал прогона.
package harness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hakastein/llamer/internal/domain"
	"github.com/spf13/pflag"
)

// Name — имя команды харнесса в окружении этапа.
const Name = "review"

// BriefEnv — переменная с путём до задания этапа.
const BriefEnv = "LLAMER_BRIEF"

// Option — флаг команды: из флагов собираются и разбор аргументов, и справка.
type Option struct {
	Flags, Hint string
	Required    bool
	Choices     []string
}

// Result — итог команды: печать или события журнала с подтверждением.
type Result struct {
	Print        *string
	Events       []domain.Event
	Confirmation string
}

func printed(s string) Result { return Result{Print: &s} }

// values — значения флагов команды по ключу в camelCase: строка, true у
// флага без значения, список строк у повторяемого; позиционные — "positionals".
type values map[string]any

func (v values) str(key string) string {
	s, _ := v[key].(string)
	return s
}

// optional — значение флага или nil, если флаг не задан.
func (v values) optional(key string) *string {
	if s, ok := v[key].(string); ok {
		return &s
	}
	return nil
}

// number — целое из значения флага.
func (v values) number(key, flag string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v.str(key)))
	if err != nil {
		return 0, domain.Refuse("%s — целое число; пришло «%s»", flag, v.str(key))
	}
	return n, nil
}

// Command — команда харнесса описывает себя сама: имя, флаги с подсказками,
// обязательность и допустимые значения.
type Command struct {
	Name, Summary, Manual string
	Options               []Option
	Prints                bool
	Positionals           string // подсказка позиционных аргументов; пусто — команда их не берёт
	build                 func(v values, c *Context) (Result, error)
}

var Booleans = []string{"true", "false"}

var flagSplit = regexp.MustCompile(`[ ,|]`)

func longFlag(flags string) string {
	for _, part := range flagSplit.Split(flags, -1) {
		if strings.HasPrefix(part, "--") {
			return part
		}
	}
	return ""
}

var dashLetter = regexp.MustCompile(`-(\w)`)

func optionKey(flags string) string {
	name := strings.TrimPrefix(longFlag(flags), "--")
	return dashLetter.ReplaceAllStringFunc(name, func(m string) string { return strings.ToUpper(m[1:]) })
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []string:
		return !slices.ContainsFunc(x, func(s string) bool { return strings.TrimSpace(s) != "" })
	}
	return v == nil
}

func (c *Command) run(v values, ctx *Context) (Result, error) {
	for _, o := range c.Options {
		flag := longFlag(o.Flags)
		value, set := v[optionKey(o.Flags)]
		if o.Required && isEmpty(value) {
			allowed := ""
			if o.Choices != nil {
				allowed = " (одно из: " + strings.Join(o.Choices, ", ") + ")"
			}
			return Result{}, domain.Refuse("нужен %s: %s%s", flag, o.Hint, allowed)
		}
		if o.Choices != nil && set && !slices.Contains(o.Choices, v.str(optionKey(o.Flags))) {
			return Result{}, domain.Refuse("%s — одно из: %s; пришло «%s»", flag, strings.Join(o.Choices, ", "), v.str(optionKey(o.Flags)))
		}
	}
	return c.build(v, ctx)
}

// parse — значения флагов команды из argv; help — просили справку.
func (c *Command) parse(argv []string) (v values, help bool, err error) {
	fs := pflag.NewFlagSet(c.Name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.BoolVarP(&help, "help", "h", false, "")
	for _, o := range c.Options {
		name := longFlag(o.Flags)[2:]
		switch {
		case strings.Contains(o.Flags, "..."):
			fs.StringArray(name, nil, "")
		case strings.Contains(o.Flags, "<"):
			fs.String(name, "", "")
		default:
			fs.Bool(name, false, "")
		}
	}
	if err := fs.Parse(argv); err != nil {
		return nil, false, domain.Refuse("%s", FlagError(err))
	}
	v = values{}
	for _, o := range c.Options {
		name, key := longFlag(o.Flags)[2:], optionKey(o.Flags)
		switch {
		case !fs.Changed(name):
		case strings.Contains(o.Flags, "..."):
			v[key], _ = fs.GetStringArray(name)
		case strings.Contains(o.Flags, "<"):
			v[key], _ = fs.GetString(name)
		default:
			v[key], _ = fs.GetBool(name)
		}
	}
	if c.Positionals != "" {
		v["positionals"] = fs.Args()
	} else if fs.NArg() > 0 {
		return nil, false, domain.Refuse("лишний аргумент без флага: «%s»", fs.Arg(0))
	}
	return v, help, nil
}

// Справка — в раскладке commander, как её печатал харнесс до переноса в LLaMer:
// этапы и тексты заданий выросли на ней.
const (
	helpWidth = 80
	minWrap   = 40
)

var wrapChunk = regexp.MustCompile(`\s*\S+`)

// boxWrap — перенос по пробелам с сохранением переводов строк (commander Help.boxWrap).
func boxWrap(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		chunks := wrapChunk.FindAllString(line, -1)
		if chunks == nil {
			lines[i] = ""
			continue
		}
		out := []string{chunks[0]}
		for _, chunk := range chunks[1:] {
			if utf8.RuneCountInString(out[len(out)-1])+utf8.RuneCountInString(chunk) <= width {
				out[len(out)-1] += chunk
			} else {
				out = append(out, strings.TrimLeftFunc(chunk, unicode.IsSpace))
			}
		}
		lines[i] = strings.Join(out, "\n")
	}
	return strings.Join(lines, "\n")
}

func optionText(o Option) string {
	text := o.Hint
	if o.Required {
		text += " [required]"
	}
	if o.Choices != nil {
		quoted := make([]string, len(o.Choices))
		for i, c := range o.Choices {
			quoted[i] = strconv.Quote(c)
		}
		text += " (choices: " + strings.Join(quoted, ", ") + ")"
	}
	return text
}

func helpOf(c *Command) string {
	type item struct{ term, text string }
	var items []item
	for _, o := range c.Options {
		items = append(items, item{o.Flags, optionText(o)})
	}
	items = append(items, item{"-h, --help", "display this message"})
	termWidth := 0
	for _, it := range items {
		termWidth = max(termWidth, utf8.RuneCountInString(it.term))
	}
	descriptionWidth := helpWidth - termWidth - 4
	lines := []string{"Usage: " + strings.Join(slices.DeleteFunc([]string{Name, c.Name, "[options]", c.Positionals}, func(s string) bool { return s == "" }), " "), ""}
	description := c.Summary
	if c.Manual != "" {
		description += "\n\n" + c.Manual
	}
	lines = append(lines, boxWrap(description, helpWidth), "", "Options:")
	for _, it := range items {
		wrapped := it.text
		if descriptionWidth >= minWrap {
			wrapped = boxWrap(it.text, descriptionWidth)
		}
		pad := strings.Repeat(" ", termWidth-utf8.RuneCountInString(it.term))
		lines = append(lines, "  "+it.term+pad+"  "+strings.ReplaceAll(wrapped, "\n", "\n"+strings.Repeat(" ", termWidth+4)))
	}
	return strings.Join(append(lines, ""), "\n")
}

func describeCommands(commands []*Command, names []string) string {
	var parts []string
	for _, c := range commands {
		if slices.Contains(names, c.Name) {
			parts = append(parts, "$ "+Name+" "+c.Name+" --help\n"+helpOf(c))
		}
	}
	return strings.Join(parts, "\n")
}

// Справки команд в задании этапа нет: команды, поля и форматы — только здесь.
func overview(commands []*Command, names []string) string {
	lines := []string{Name + " — record review events into the run journal.", "", "Stage commands:"}
	for _, c := range commands {
		if slices.Contains(names, c.Name) {
			lines = append(lines, "  "+c.Name+"  "+c.Summary)
		}
	}
	lines = append(lines, "", describeCommands(commands, names), "Command help: "+Name+" <command> --help")
	return strings.Join(lines, "\n")
}

// dispatch — разбор вызова. Команда вне списка этапа отбивается здесь, до
// исполнения: разрешения песочницы драйвера только объявляют команды, а держит
// набор команд этапа харнесс.
func dispatch(argv []string, commands []*Command, ctx *Context) (*Command, Result, error) {
	if len(argv) == 0 || argv[0] == "" || argv[0] == "--help" || argv[0] == "-h" {
		return nil, printed(overview(commands, ctx.Commands())), nil
	}
	name := argv[0]
	command := commandByName(commands, name)
	if command == nil || !slices.Contains(ctx.Commands(), name) {
		return nil, Result{}, domain.Refuse("команды «%s» у этапа «%s» нет; его команды: %s", name, ctx.Stage(), strings.Join(ctx.Commands(), ", "))
	}
	v, help, err := command.parse(argv[1:])
	if err != nil {
		return nil, Result{}, err
	}
	if help {
		return nil, printed(helpOf(command)), nil
	}
	result, err := command.run(v, ctx)
	return command, result, err
}

func commandByName(commands []*Command, name string) *Command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Main — `review` в окружении этапа; возвращает код выхода. Отказ агенту и
// сбой харнесса — разные исходы: первый чинится исправленным вызовом, во втором
// чинить вызов нечего.
func Main(argv []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(stderr, "сбой харнесса: %v\n", p)
			code = 2
		}
	}()
	err := run(argv, stdout)
	if err == nil {
		return 0
	}
	if r, ok := err.(*domain.Refusal); ok {
		fmt.Fprintln(stderr, "отказ: "+r.Message)
		return 1
	}
	fmt.Fprintln(stderr, "сбой харнесса: "+err.Error())
	return 2
}

func run(argv []string, stdout io.Writer) error {
	brief, err := readBrief(os.Getenv(BriefEnv))
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx := &Context{brief: brief, journal: domain.OpenJournal(brief.JournalDir, brief.Stage), cwd: cwd}
	command, result, err := dispatch(argv, Commands, ctx)
	if err != nil {
		return err
	}
	if result.Print != nil {
		_, err := fmt.Fprintln(stdout, *result.Print)
		return err
	}
	for _, e := range result.Events {
		h := e.Header()
		h.Stage, h.Driver = brief.Stage, brief.Driver
		if err := ctx.journal.Append(e); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(stdout, "записано: "+command.Name+result.Confirmation)
	return err
}

// FlagError — отказ pflag по-русски: его тексты английские, а читает их человек или агент этапа.
func FlagError(err error) string {
	var unknown *pflag.NotExistError
	var noValue *pflag.ValueRequiredError
	var invalid *pflag.InvalidValueError
	switch {
	case errors.As(err, &unknown):
		if name := unknown.GetSpecifiedName(); name != "" {
			return "неизвестный флаг --" + name
		}
		return "неизвестный флаг -" + unknown.GetSpecifiedShortnames()
	case errors.As(err, &noValue):
		return "флагу --" + noValue.GetFlag().Name + " нужно значение"
	case errors.As(err, &invalid):
		return "--" + invalid.GetFlag().Name + ": значение «" + invalid.GetValue() + "» не подходит"
	}
	return "флаги не разобраны: " + err.Error()
}
