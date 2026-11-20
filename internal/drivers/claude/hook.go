package claude

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hakastein/llamer/internal/drivers"
)

// hookCommand — скрытая подкоманда llamer: хук PreToolUse этапа claude.
const hookCommand = "__claude-hook"

// callsFile — вызовы прогона для хука: хук — отдельный процесс, его команда и окружение
// одни на все этапы, и он находит их в CLAUDE_CONFIG_DIR этапа.
const callsFile = "llamer-calls.json"

const (
	// Слово без кавычек — только из букв, которые оболочка не раскрывает.
	word = `[\w./:,@+=%-]+`
	// Одинарные кавычки, склеенные апострофом `\'` или `"'"`: внутри них оболочка
	// не трогает ничего, переводы строк тоже.
	quotedWord = `'[^']*'(?:(?:\\'|"'")'[^']*')*`
	// Цикл по литеральным путям: в слове списка нет ничего, что оболочка раскрыла бы.
	loopPath = `[A-Za-z0-9_./@+-]+`
	loopSep  = `[ \t]*[;\n]\s*`
	loopVar  = `[A-Za-z_]\w*`
	loopArg  = `(?:"\$` + loopVar + `"|\$` + loopVar + `|` + loopPath + `)`
)

var loopVarRef = regexp.MustCompile(`\$(` + loopVar + `)`)

// forms — формы разрешённых вызовов; nil — такой формы у прогона нет.
type forms struct{ call, loop *regexp.Regexp }

func compileForms(calls drivers.Calls) (forms, error) {
	var f forms
	var err error
	if len(calls.Commands) > 0 {
		f.call, err = regexp.Compile(`^(?P<head>` + strings.Join(calls.Commands, "|") + `)(?:[ \t]+(?:` + quotedWord + `|` + word + `))*[ \t]*$`)
		if err != nil {
			return f, err
		}
	}
	if len(calls.Loops) > 0 {
		f.loop, err = regexp.Compile(`^for[ \t]+(?P<var>` + loopVar + `)[ \t]+in(?:[ \t]+` + loopPath + `)+` + loopSep + `do\s+(?P<head>` +
			strings.Join(calls.Loops, "|") + `)(?P<args>(?:[ \t]+` + loopArg + `)+)` + loopSep + `done(?:[ \t]*\|[ \t]*(?:head|tail) -\d+)?\s*$`)
	}
	return f, err
}

// allowed — голова разрешённого вызова. Вызов одной простой командой без раскрытий
// Claude Code пропускает правилом `Bash(<команда> *)` сам, но команду длиннее
// 10 000 символов он не разбирает вовсе и в dontAsk отклоняет: батч из
// полутора десятков находок пропадал целиком. Хук разрешает её по той же
// форме, что и правило. Ещё — цикл по путям: подстановку `"$f"` Claude Code
// не разбирает и правилом не пропускает.
func (f forms) allowed(command string) (string, bool) {
	if f.call != nil {
		if m := f.call.FindStringSubmatch(command); m != nil {
			return m[f.call.SubexpIndex("head")], true
		}
	}
	if f.loop == nil {
		return "", false
	}
	m := f.loop.FindStringSubmatch(command)
	if m == nil {
		return "", false
	}
	// Подставляется только переменная самого цикла.
	for _, ref := range loopVarRef.FindAllStringSubmatch(m[f.loop.SubexpIndex("args")], -1) {
		if ref[1] != m[f.loop.SubexpIndex("var")] {
			return "", false
		}
	}
	return m[f.loop.SubexpIndex("head")], true
}

// hookMain — хук PreToolUse этапа claude: разрешает вызовы прогона по их форме;
// молчание — решение за правилами разрешений.
func hookMain(stdin io.Reader, stdout io.Writer) error {
	data, err := os.ReadFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), callsFile))
	if err != nil {
		return err
	}
	var calls drivers.Calls
	if err := json.Unmarshal(data, &calls); err != nil {
		return err
	}
	f, err := compileForms(calls)
	if err != nil {
		return err
	}
	var input struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		return err
	}
	head, ok := f.allowed(input.ToolInput.Command)
	if !ok {
		return nil
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "allow", "permissionDecisionReason": "вызов " + strings.Fields(head)[0]}})
}
