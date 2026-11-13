package drivers

import (
	"encoding/json"
	"io"
	"regexp"

	"github.com/hakastein/llamer/internal/harness"
)

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

var (
	harnessCallForm = regexp.MustCompile(`^` + harness.Name + ` [a-z]+(?:[ \t]+(?:` + quotedWord + `|` + word + `))*[ \t]*$`)
	harnessLoop     = regexp.MustCompile(`^for[ \t]+(` + loopVar + `)[ \t]+in(?:[ \t]+` + loopPath + `)+` + loopSep + `do\s+` +
		harness.Name + ` diff((?:[ \t]+` + loopArg + `)+)` + loopSep + `done(?:[ \t]*\|[ \t]*(?:head|tail) -\d+)?\s*$`)
	loopVarRef = regexp.MustCompile(`\$(` + loopVar + `)`)
)

// IsHarnessCall — вызов харнесса одной простой командой без раскрытий. Такой
// Claude Code пропускает правилом `Bash(review … *)` сам, но команду длиннее
// 10 000 символов он не разбирает вовсе и в dontAsk отклоняет: батч из
// полутора десятков находок пропадал целиком. Хук разрешает её по той же
// форме, что и правило. Ещё — цикл `review diff` по путям: подстановку `"$f"`
// Claude Code не разбирает и правилом `Bash(review diff *)` не пропускает.
func IsHarnessCall(command string) bool {
	if harnessCallForm.MatchString(command) {
		return true
	}
	m := harnessLoop.FindStringSubmatch(command)
	if m == nil {
		return false
	}
	// Подставляется только переменная самого цикла.
	for _, ref := range loopVarRef.FindAllStringSubmatch(m[2], -1) {
		if ref[1] != m[1] {
			return false
		}
	}
	return true
}

// HookMain — хук PreToolUse этапа claude: разрешает вызов харнесса; молчание —
// решение за правилами разрешений.
func HookMain(stdin io.Reader, stdout io.Writer) error {
	var input struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		return err
	}
	if !IsHarnessCall(input.ToolInput.Command) {
		return nil
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "allow", "permissionDecisionReason": "вызов " + harness.Name}})
}
