package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/git"
	"github.com/hakastein/llamer/internal/materials"
)

var (
	natures   = []string{"mechanical", "behavioral"}
	risks     = []string{"low", "medium", "high"}
	levelHint = fmt.Sprintf("how deep the review goes: 1 (cheapest) to %d (priciest)", config.LevelCount)
)

// Commands — команды харнесса в порядке справки.
var Commands = []*Command{
	{
		Name: "assess", Summary: "record the verdict on the request: nature, risk and review depth",
		Options: []Option{
			{Flags: "--nature <kind>", Hint: "does behaviour change", Required: true, Choices: natures},
			{Flags: "--risk <level>", Hint: "the same assessment in three steps", Required: true, Choices: risks},
			{Flags: "--level <number>", Hint: levelHint, Required: true},
			{Flags: "--reason <text>", Hint: "what justifies the verdicts", Required: true},
		},
		build: assess,
	},
	{
		Name: "batch", Summary: "record several journal events in one call",
		Manual: "Types may be mixed in one array. A refused event is not recorded and comes back" +
			" as a warning naming it — repeat that one fixed, the rest are already in." +
			" Only unparsable JSON refuses the whole call. Finding numbers for `amend` and" +
			" `dupe` come from `list`: stages record in parallel, and the order settles in the" +
			" journal, not in this answer.",
		Options: []Option{{Flags: "--json <array>", Required: true,
			Hint: `a JSON array of events: {"type":"finding",…}, fields named as the single command's flags in camelCase`}},
	},
	{
		Name: "finding", Summary: "record a remark on the request, anchored to diff lines",
		Manual: "Highlight everything the remark is about — the whole condition, call, branch or" +
			" method; that is where the thread anchors.\n" +
			"For DELETED code use --deleted, anchored on the old side; otherwise the thread lands" +
			" on the new side, on whatever replaced it.",
		Options: []Option{
			{Flags: "--file <path...>", Hint: "a repo-root-relative path, as in the diff", Required: true},
			{Flags: "--line <number>", Hint: "first line of the fragment", Required: true},
			{Flags: "--line-end <number>", Hint: "last line of it; no default, so repeat --line for a one-line remark", Required: true},
			{Flags: "--text <text>", Hint: "what is wrong and how to solve it", Required: true},
			{Flags: "--severity <level>", Hint: "the level the consequence puts the finding on", Required: true, Choices: domain.Severities},
			{Flags: "--severity-reason <text>", Hint: "the one consequence that puts it there, distinct from --text", Required: true},
			{Flags: "--related <list>", Hint: "what the remark rests on: «path», «path:line» or «path:line-line»," +
				" comma-separated; anything else in the value is dropped silently. Lines are" +
				" from the new version even with --deleted"},
			{Flags: "--trace <text>", Hint: "the chain of reasoning, for a finding about interaction"},
			{Flags: "--suggestion <code>", Hint: "replaces the WHOLE fragment, any length; the button applies it unread, so" +
				" offer one only when you know the whole fix. Markup is added for you"},
			{Flags: "--deleted", Hint: "the fragment is deleted lines: --line/--line-end are from the version" +
				" BEFORE the request (git show <base>:<file>)"},
		},
		build: finding,
	},
	{
		Name: "amend", Summary: "correct a finding you already recorded, by its number",
		Manual: "Only the named fields change; the number stays, so verdicts and duplicate" +
			" marks keep pointing at it. Amends only findings recorded by your own stage.",
		Options: []Option{
			{Flags: "--id <number>", Hint: "the number the batch report printed", Required: true},
			{Flags: "--text <text>", Hint: "what is wrong and how to solve it"},
			{Flags: "--severity <level>", Hint: "the level the consequence puts the finding on", Choices: domain.Severities},
			{Flags: "--severity-reason <text>", Hint: "what justifies the severity"},
			{Flags: "--related <list>", Hint: "where you looked, «path:line» comma-separated"},
			{Flags: "--suggestion <code>", Hint: "a complete replacement for the highlighted fragment"},
		},
		build: amend,
	},
	{
		Name: "verdict", Summary: "pass a verdict on a recorded finding: stands or refuted",
		Options: []Option{
			{Flags: "--id <number>", Hint: "the #N of the finding from the list in your task", Required: true},
			{Flags: "--refuted <bool>", Hint: "does the finding fall", Required: true, Choices: Booleans},
			{Flags: "--reason <text>", Required: true, Hint: "the decisive fact with file and line: what refutes it, or why it stands" +
				" and which step of the scale you confirmed"},
			{Flags: "--severity <level>", Hint: "the revised severity; goes only together with --severity-reason", Choices: domain.Severities},
			{Flags: "--severity-reason <text>", Hint: "what the revised severity rests on"},
		},
		build: verdict,
	},
	{
		Name: "dupe", Summary: "mark a finding as a duplicate of another one",
		Manual: "Same defect as another finding: same place, same cause. The marked one drops" +
			" out — --of should be the better-written of the two.",
		Options: []Option{
			{Flags: "--id <number>", Hint: "the number of the duplicate finding", Required: true},
			{Flags: "--of <number>", Hint: "the number of the finding that stays", Required: true},
			{Flags: "--reason <text>", Hint: "why this is the same place and the same cause", Required: true},
		},
		build: dupe,
	},
	{
		Name: "resolve", Summary: "close an open review thread the current code has settled",
		Manual: "Closes a thread of ours that earlier runs left open. Treat the remark as a" +
			" REQUIREMENT: it is settled once the current code satisfies it, wherever that" +
			" code now lives.",
		Options: []Option{
			{Flags: "--id <id>", Hint: "the thread id from the list in your task", Required: true},
			{Flags: "--reason <text>", Hint: "what exactly settled the remark in this revision", Required: true},
		},
		build: resolve,
	},
	{
		Name: "thread", Summary: "print a request thread in full, with the author's replies", Prints: true,
		Options: []Option{{Flags: "--id <id>", Hint: "the thread id from the pack", Required: true}},
		build:   thread,
	},
	{
		Name: "blame", Summary: "who last changed a line and with what commit message", Prints: true,
		Options: []Option{
			{Flags: "--file <path>", Hint: "a repo-root-relative path", Required: true},
			{Flags: "--line <number>", Hint: "the line in the current version", Required: true},
		},
		build: blame,
	},
	{
		Name: "summary", Summary: "close the review with the verdict on the whole request",
		Options: []Option{{Flags: "--text <text>", Hint: "the verdict on the request as a whole", Required: true}},
		build: func(v values, _ *Context) (Result, error) {
			return Result{Events: []domain.Event{&domain.Summary{Text: v.str("text")}}}, nil
		},
	},
	{
		Name: "stop", Summary: "abort the stage and say why",
		Options: []Option{{Flags: "--reason <text>", Hint: "why the review was aborted", Required: true}},
		build: func(v values, c *Context) (Result, error) {
			return Result{Events: []domain.Event{&domain.Stop{Name: c.brief.Name, Reason: v.str("reason")}}}, nil
		},
	},
	{
		Name: "list", Summary: "print the run journal: number, severity and place of every finding", Prints: true,
		Manual: "The whole journal, every stage, including findings already dropped from" +
			" publication ([refuted], [duplicate of #N]). A write confirms without a number," +
			" so this is where a number comes from outside a batch report.",
		build: list,
	},
	{
		Name: "diff", Summary: "the task's diff of the given files, numbered and widened as in the pack's diff", Prints: true,
		Manual:      "Several paths per call. Without paths: every changed file with its status and line counts.",
		Positionals: "[path...]",
		build:       diff,
	},
}

func init() { commandByName(Commands, "batch").build = batch }

// WriteCommands — команды, которые пишут в журнал: все, кроме печатающих ответ.
var WriteCommands = func() []string {
	var out []string
	for _, c := range Commands {
		if !c.Prints {
			out = append(out, c.Name)
		}
	}
	return out
}()

func assess(v values, _ *Context) (Result, error) {
	level, err := strconv.Atoi(strings.TrimSpace(v.str("level")))
	if err != nil || level < 1 || level > config.LevelCount {
		return Result{}, domain.Refuse("--level — %s; пришло «%s»", levelHint, v.str("level"))
	}
	return Result{Events: []domain.Event{&domain.Assessment{Nature: v.str("nature"), Risk: v.str("risk"), Reason: v.str("reason"), Level: level}}}, nil
}

const batchWindow = 40

// around — место ошибки разбора в тексте: агент иначе ищет позицию счётом
// символов и тратит на это ход.
func around(raw string, offset int) string {
	before, after := []rune(raw[:offset]), []rune(raw[offset:])
	return "\nЗдесь: …" + string(before[max(0, len(before)-batchWindow):]) + "<-- ЗДЕСЬ -->" + string(after[:min(len(after), batchWindow)]) + "…"
}

var verdictStatus = map[bool]string{true: "опровергнута", false: "в силе"}

// scalar — значение поля батча строкой, как его дал бы разбор аргументов.
func scalar(x any) (string, bool) {
	switch t := x.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// fromJSON — поля события батча в значениях флагов команды: строки,
// `<name...>` — список, флаг без значения (`--deleted`) — булев.
func fromJSON(c *Command, event map[string]any) (values, error) {
	v := values{}
	for _, o := range c.Options {
		key := optionKey(o.Flags)
		raw := event[key]
		if raw == nil {
			continue
		}
		switch {
		case strings.Contains(o.Flags, "..."):
			items, isList := raw.([]any)
			if !isList {
				items = []any{raw}
			}
			list := []string{}
			for _, item := range items {
				s, ok := scalar(item)
				if !ok {
					return nil, domain.Refuse("поле %s — строка или список строк", key)
				}
				list = append(list, s)
			}
			v[key] = list
		case strings.Contains(o.Flags, "<"):
			s, ok := scalar(raw)
			if !ok {
				return nil, domain.Refuse("поле %s — строка или число", key)
			}
			v[key] = s
		default:
			b, ok := raw.(bool)
			if !ok {
				return nil, domain.Refuse("поле %s — true или false", key)
			}
			v[key] = b
		}
	}
	return v, nil
}

// describe — строка подтверждения батча о записанном событии.
func describe(event domain.Event) string {
	var id, detail, place, severity, text string
	switch e := event.(type) {
	case *domain.FindingEvent:
		place = fmt.Sprintf(" %s:%d", e.File, e.Line)
		if e.LineEnd != e.Line {
			place += fmt.Sprintf("-%d", e.LineEnd)
		}
		severity, text = e.Severity, e.Text
	case *domain.Amend:
		id = strconv.Itoa(e.ID)
		if e.Severity != nil {
			severity = *e.Severity
		}
		if e.Text != nil {
			text = *e.Text
		}
	case *domain.Verdict:
		id, detail, text = strconv.Itoa(e.ID), " "+verdictStatus[e.Refuted], e.Reason
		if e.Severity != nil {
			severity = *e.Severity
		}
	case *domain.Dupe:
		id, detail, text = strconv.Itoa(e.ID), fmt.Sprintf(" повтор #%d", e.Of), e.Reason
	case *domain.Assessment:
		text = e.Reason
	case *domain.Resolve:
		id, text = e.ID, e.Reason
	case *domain.Summary:
		text = e.Text
	case *domain.Stop:
		text = e.Reason
	}
	line := event.Kind()
	if id != "" {
		line += " #" + id
	}
	line += detail + place
	if severity != "" {
		line += " " + severity
	}
	if text != "" {
		line += " «" + cut(strings.Join(strings.Fields(text), " "), 100) + "»"
	}
	return line
}

// cut — первые n символов текста.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func batch(v values, c *Context) (Result, error) {
	raw := v.str("json")
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var parsed any
	err := dec.Decode(&parsed)
	if err == nil && dec.More() {
		err = errors.New("после JSON лишний текст")
	}
	if err != nil {
		where := ""
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			where = around(raw, int(syntax.Offset))
		}
		return Result{}, domain.Refuse("--json — не JSON (%s), ничего не записано.%s", err, where)
	}
	events, ok := parsed.([]any)
	if !ok {
		events = []any{parsed}
	}
	if len(events) == 0 {
		return Result{}, domain.Refuse("в --json нет ни одного события")
	}
	var lines, warnings []string
	var recorded []domain.Event
	for i, item := range events {
		warn := func(message string) {
			warnings = append(warnings, fmt.Sprintf("  элемент %d: %s", i+1, message))
		}
		event, ok := item.(map[string]any)
		if !ok {
			text, _ := json.Marshal(item)
			warn("элемент — не объект события (" + string(text) + "), не записан")
			continue
		}
		typ, _ := scalar(event["type"])
		var command *Command
		if slices.Contains(c.Commands(), typ) {
			command = commandByName(Commands, typ)
		}
		if command == nil {
			warn("типа «" + typ + "» среди команд этапа нет (" + strings.Join(c.Commands(), ", ") + ") — не записано")
			continue
		}
		if command.Prints {
			warn("команда " + typ + " печатает ответ и ничего не пишет — вызови её отдельно")
			continue
		}
		vals, err := fromJSON(command, event)
		built := Result{}
		if err == nil {
			built, err = command.run(vals, c)
		}
		if r, ok := err.(*domain.Refusal); ok {
			// Отбитое не записывается: позицию для GitLab по такой строке не собрать.
			warn(r.Message + " — не записано, повтори исправленным")
			continue
		}
		if err != nil {
			return Result{}, err
		}
		lines = append(lines, describe(built.Events[0]))
		recorded = append(recorded, built.Events...)
	}
	if len(warnings) > 0 {
		lines = append(append(lines, "предупреждения:"), warnings...)
	}
	return Result{Events: recorded, Confirmation: "\n" + strings.Join(lines, "\n")}, nil
}

func finding(v values, c *Context) (Result, error) {
	related := domain.ParseRelated(v.str("related"))
	if c.brief.RelatedRequired && len(related) == 0 {
		return Result{}, domain.Refuse("нужен --related: куда ты смотрел, устанавливая находку" +
			" («путь:строка» через запятую). Если находка очевидна по одной строке, назови эту строку.")
	}
	line, err := v.number("line", "--line")
	if err != nil {
		return Result{}, err
	}
	lineEnd, err := v.number("lineEnd", "--line-end")
	if err != nil {
		return Result{}, err
	}
	files, _ := v["file"].([]string)
	f := &domain.FindingEvent{
		File: files[0], Line: line, LineEnd: lineEnd, Deleted: v["deleted"] == true, Text: v.str("text"), Severity: v.str("severity"),
		SeverityReason: v.str("severityReason"), Suggestion: v.optional("suggestion"), Related: related, Trace: v.optional("trace"),
	}
	diff, err := c.Diff()
	if err != nil {
		return Result{}, err
	}
	if err := f.AssertAnchored(diff); err != nil {
		return Result{}, err
	}
	if f.Suggestion != nil {
		lines, err := c.ReadFileLines(f.File)
		if err != nil {
			return Result{}, err
		}
		if err := f.AssertSuggestionApplies(lines, *f.Suggestion); err != nil {
			return Result{}, err
		}
	}
	return Result{Events: []domain.Event{f}, Confirmation: " " + f.At()}, nil
}

func amend(v values, c *Context) (Result, error) {
	id, err := v.number("id", "--id")
	if err != nil {
		return Result{}, err
	}
	f := c.State().FindingByID(id)
	if f == nil {
		return Result{}, domain.Refuse("находки #%d в журнале нет — сверь номера через list", id)
	}
	if f.Stage != c.Stage() {
		return Result{}, domain.Refuse("находку #%d записал этап «%s», а не «%s»", id, f.Stage, c.Stage())
	}
	event := &domain.Amend{ID: id, Text: v.optional("text"), Severity: v.optional("severity"),
		SeverityReason: v.optional("severityReason"), Suggestion: v.optional("suggestion")}
	if r := v.optional("related"); r != nil {
		related := domain.ParseRelated(*r)
		event.Related = &related
	}
	if event.Text == nil && event.Severity == nil && event.SeverityReason == nil && event.Related == nil && event.Suggestion == nil {
		return Result{}, domain.Refuse("назови хотя бы одно поле, которое меняешь")
	}
	if event.Suggestion != nil {
		lines, err := c.ReadFileLines(f.File)
		if err != nil {
			return Result{}, err
		}
		if err := f.AssertSuggestionApplies(lines, *event.Suggestion); err != nil {
			return Result{}, err
		}
	}
	return Result{Events: []domain.Event{event}}, nil
}

func verdict(v values, c *Context) (Result, error) {
	id, err := v.number("id", "--id")
	if err != nil {
		return Result{}, err
	}
	target := c.State().FindingByID(id)
	if target == nil {
		return Result{}, domain.Refuse("находки с номером %d нет; список — %s list", id, Name)
	}
	if target.DuplicateOf != 0 {
		return Result{}, domain.Refuse("находка #%d помечена повтором #%d и до реквеста не дойдёт: вердикт ей не нужен."+
			" Суди находки из списка в своём задании", id, target.DuplicateOf)
	}
	if v.str("severity") != "" && v.str("severityReason") == "" {
		return Result{}, domain.Refuse("вместе с --severity нужен --severity-reason")
	}
	return Result{Events: []domain.Event{&domain.Verdict{ID: id, Refuted: v.str("refuted") == "true", Reason: v.str("reason"),
		Severity: v.optional("severity"), SeverityReason: v.optional("severityReason")}}}, nil
}

func dupe(v values, c *Context) (Result, error) {
	find := func(key, flag string) (int, *domain.Finding, error) {
		n, err := v.number(key, flag)
		if err != nil {
			return 0, nil, err
		}
		f := c.State().FindingByID(n)
		if f == nil {
			return 0, nil, domain.Refuse("%s: находки с номером %d нет; список — %s list", flag, n, Name)
		}
		return n, f, nil
	}
	id, duplicate, err := find("id", "--id")
	if err != nil {
		return Result{}, err
	}
	of, original, err := find("of", "--of")
	if err != nil {
		return Result{}, err
	}
	if id == of {
		return Result{}, domain.Refuse("--id и --of совпадают: находка не может повторять саму себя")
	}
	if duplicate.DuplicateOf != 0 {
		return Result{}, domain.Refuse("находка #%d уже помечена повтором #%d", id, duplicate.DuplicateOf)
	}
	if original.DuplicateOf != 0 {
		return Result{}, domain.Refuse("#%d сама помечена повтором #%d: в --of ставится оставляемая находка, а не другой повтор — вероятно, это #%d",
			of, original.DuplicateOf, original.DuplicateOf)
	}
	return Result{Events: []domain.Event{&domain.Dupe{ID: id, Of: of, Reason: v.str("reason")}}}, nil
}

func threadByID(c *Context, id string) *materials.Thread {
	for i, t := range c.brief.Threads {
		if t.ID == id {
			return &c.brief.Threads[i]
		}
	}
	return nil
}

func resolve(v values, c *Context) (Result, error) {
	t := threadByID(c, v.str("id"))
	if t == nil {
		var open []string
		for _, item := range c.brief.Threads {
			open = append(open, item.ID+" — "+item.At)
		}
		return Result{}, domain.Refuse("треда «%s» среди открытых тредов прогона нет. Открытые:\n%s", v.str("id"), strings.Join(open, "\n"))
	}
	return Result{Events: []domain.Event{&domain.Resolve{ID: t.ID, Place: t.At, Reason: v.str("reason")}}, Confirmation: " " + t.At}, nil
}

func thread(v values, c *Context) (Result, error) {
	t := threadByID(c, v.str("id"))
	if t == nil {
		return Result{}, domain.Refuse("треда %s нет среди открытых — id есть в задании", v.str("id"))
	}
	text := t.Full.Text
	if text == "" {
		text = t.Text
	}
	lines := []string{t.At + " [" + t.ID + "]", text}
	for _, r := range t.Full.Replies {
		lines = append(lines, "  ответ автора: "+r)
	}
	return printed(strings.Join(lines, "\n")), nil
}

func blame(v values, _ *Context) (Result, error) {
	file, line := v.str("file"), v.str("line")
	out, err := git.Raw([]string{"blame", "-L", line + "," + line, "--porcelain", "--", file}, "")
	if err != nil {
		return Result{}, domain.Refuse("git blame не ответил по %s:%s (%s)", file, line, git.FirstLine(err))
	}
	field := func(name string) string {
		if m := regexp.MustCompile(`(?m)^` + name + ` (.+)$`).FindStringSubmatch(out); m != nil {
			return m[1]
		}
		return ""
	}
	date := ""
	if seconds, err := strconv.ParseInt(field("author-time"), 10, 64); err == nil {
		date = time.Unix(seconds, 0).UTC().Format("2006-01-02")
	}
	return printed(out[:min(8, len(out))] + " " + field("author") + " " + date + " — " + field("summary")), nil
}

func list(_ values, c *Context) (Result, error) {
	state := c.State()
	var lines []string
	for _, f := range state.Findings {
		mark := f.Severity
		if f.DuplicateOf != 0 {
			mark = fmt.Sprintf("повтор #%d", f.DuplicateOf)
		} else if f.Refuted {
			mark = "опровергнута"
		}
		lines = append(lines, fmt.Sprintf("%d. [%s] %s — %s", f.ID, mark, f.At(), cut(strings.Join(strings.Fields(f.Text), " "), 120)))
	}
	if len(lines) == 0 {
		lines = append(lines, "находок пока нет")
	}
	if state.SkippedLines > 0 {
		lines = append(lines, fmt.Sprintf("пропущено битых строк журнала: %d", state.SkippedLines))
	}
	return printed(strings.Join(lines, "\n")), nil
}

var fileStatus = map[byte]string{'A': "добавлен", 'M': "изменён", 'D': "удалён", 'R': "переименован"}

type changed struct{ line, from string }

// changedFiles — файлы изменений задачи по ревизиям задания: путь → строка перечня и путь до переименования.
func changedFiles(revs []string, cwd string) ([]string, map[string]changed, error) {
	lines := func(args ...string) ([]string, error) {
		out, err := git.Git(append(append([]string{"diff"}, args...), revs...), cwd)
		if err != nil {
			return nil, err
		}
		var rows []string
		for _, l := range strings.Split(out, "\n") {
			if l != "" {
				rows = append(rows, l)
			}
		}
		return rows, nil
	}
	// Обе выдачи — из одной очереди git diff, порядок файлов в них один.
	counts, err := lines("--numstat")
	if err != nil {
		return nil, nil, err
	}
	statuses, err := lines("--name-status")
	if err != nil {
		return nil, nil, err
	}
	var order []string
	files := map[string]changed{}
	for i, row := range statuses {
		parts := strings.Split(row, "\t")
		status, path := parts[0], ""
		if len(parts) > 1 {
			path = parts[1]
		}
		numbers := strings.Split(counts[i], "\t")
		kind, ok := fileStatus[status[0]]
		if !ok {
			kind = status
		}
		count := "бинарный"
		if numbers[0] != "-" {
			count = "+" + numbers[0] + " −" + numbers[1]
		}
		file, from := path, ""
		if len(parts) > 2 {
			file, from = parts[2], " из "+path
		}
		if _, seen := files[file]; !seen {
			order = append(order, file)
		}
		files[file] = changed{file + " — " + kind + from + ", " + count, path}
	}
	return order, files, nil
}

func diff(v values, c *Context) (Result, error) {
	revs := c.brief.Revs
	order, files, err := changedFiles(revs, c.cwd)
	if err != nil {
		return Result{}, err
	}
	paths, _ := v["positionals"].([]string)
	if len(paths) == 0 {
		var lines []string
		for _, f := range order {
			lines = append(lines, files[f].line)
		}
		if len(lines) == 0 {
			return printed("изменений задачи нет"), nil
		}
		return printed(strings.Join(lines, "\n")), nil
	}
	var out []string
	for _, path := range paths {
		f, ok := files[path]
		if !ok {
			out = append(out, path+": среди изменений задачи такого файла нет; перечень — review diff без аргументов")
			continue
		}
		// Тот же вызов, что строит дифф пакета, с первой — полной — шириной контекста.
		d := materials.NewDiff(revs[0], []string{path}, revs, c.cwd, map[string]string{path: f.from})
		text, err := d.Text(materials.DiffWidths[0])
		if err != nil {
			return Result{}, err
		}
		out = append(out, strings.TrimRightFunc(materials.NumberDiffLines(text), unicode.IsSpace))
	}
	return printed(strings.Join(out, "\n")), nil
}
