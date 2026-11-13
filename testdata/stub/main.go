// Заглушка драйвера этапа — `claude` или `codex` по имени, под которым её
// позвали. Те же аргументы и stdin, что у настоящего; отчёт — теми же командами
// харнесса `review` из PATH. С чем этап был запущен, пишется в
// $TMPDIR/launches.jsonl: окружение этапа — белый список, и TMPDIR в нём есть.
//
// Поведение задают переменные, которые тест передаёт конфигом через
// drivers.<имя>.env: STUB_FAIL_STAGE, STUB_STOP_STAGE, STUB_NO_SUMMARY,
// STUB_DENY_STAGE, STUB_HANG_STAGE, STUB_SKEPTIC_REFUTES — значение — этап;
// STUB_EVENTS — строки потока, которые заглушка отдаёт этапу до своих;
// STUB_SESSION — строки журнала сессии codex вместо обычных.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Token — секрет, который заглушка «вычитывает из дерева»: наружу он должен уйти вымаранным.
const Token = "glpat-...leak"

type brief struct {
	Stage    string           `json:"stage"`
	DiffPath string           `json:"diffPath"`
	Commands []string         `json:"commands"`
	Threads  []map[string]any `json:"threads"`
}

func main() {
	driver := filepath.Base(os.Args[0])
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("0.0.0 (заглушка " + driver + ")")
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	var b brief
	data, err := os.ReadFile(os.Getenv("LLAMER_BRIEF"))
	must(err)
	must(json.Unmarshal(data, &b))
	if driver == "codex" {
		os.Exit(codex(b, args, string(input)))
	}
	os.Exit(claude(b, args, string(input)))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func emit(event any) {
	line, err := json.Marshal(event)
	must(err)
	os.Stdout.Write(append(line, '\n'))
}

type m = map[string]any

func review(args ...string) (string, int) {
	cmd := exec.Command("review", args...)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	must(err)
	return string(out), 0
}

func output(args ...string) string {
	out, _ := exec.Command("review", args...).Output()
	return string(out)
}

func readOr(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return string(data)
}

func listDir(dir string) any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func envNames() []string {
	var names []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func record(launch m) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "launches.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	must(err)
	defer f.Close()
	line, err := json.Marshal(launch)
	must(err)
	_, err = f.Write(append(line, '\n'))
	must(err)
}

// extraEvents — строки STUB_EVENTS: этап получает их до событий заглушки.
func extraEvents() {
	for _, line := range strings.Split(os.Getenv("STUB_EVENTS"), "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Println(line)
		}
	}
}

func claude(b brief, argv []string, prompt string) int {
	after := func(flag string) string {
		if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	var shared, skills any
	if path := after("--append-system-prompt-file"); path != "" {
		shared = readOr(path)
	}
	if dir := after("--plugin-dir"); dir != "" {
		skills = listDir(filepath.Join(dir, "skills"))
	}
	record(m{"driver": "claude", "stage": b.Stage, "argv": argv, "env": envNames(), "prompt": prompt, "commands": b.Commands,
		"help": output("--help"), "diff": output("diff", "src/Order.php"), "shared": shared,
		"configDir": os.Getenv("CLAUDE_CONFIG_DIR"), "skills": skills})

	// Сессия, как у Claude Code: projects/<каталог>/<id>.jsonl конфига, в ней — вывод команды с токеном.
	sessionID := "stub-" + b.Stage
	project := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-stub-tree")
	must(os.MkdirAll(project, 0o777))
	must(os.WriteFile(filepath.Join(project, sessionID+".jsonl"), []byte(`{"type":"user","text":"токен `+Token+`"}`+"\n"), 0o666))
	emit(m{"type": "system", "subtype": "init", "model": "stub", "session_id": sessionID})
	// Лимиты подписки — дословное событие живого claude 2.1.316.
	fmt.Println(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794480600,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.45,"resetsAt":1794480600,"status":"allowed"},"seven_day":{"utilization":0.58,"resetsAt":1794787200,"status":"allowed"}}},"uuid":"a7255113-cdfd-4eac-9f94-7a154dcadb53","session_id":"f66bac7e-9b13-4b63-82a4-ae9fce05e825"}`)
	extraEvents()

	// Этап с запущенной командой повисает — его прерывает тест; через минуту оба выходят сами.
	if os.Getenv("STUB_HANG_STAGE") == b.Stage {
		command := exec.Command("sleep", "60")
		must(command.Start())
		hang := filepath.Join(os.TempDir(), "hang.json")
		data, _ := json.Marshal(m{"llamer": os.Getppid(), "stage": os.Getpid(), "command": command.Process.Pid})
		must(os.WriteFile(hang+".part", data, 0o666))
		must(os.Rename(hang+".part", hang))
		time.Sleep(time.Minute)
		return 1
	}
	if os.Getenv("STUB_FAIL_STAGE") == b.Stage {
		emit(m{"type": "result", "is_error": true, "result": "заглушка: этап сорвался"})
		return 1
	}
	emit(m{"type": "assistant", "message": m{"id": "m1", "content": []m{{"type": "text", "text": "заглушка этапа " + b.Stage}}}})
	// Вызов харнесса отклонён правами, как батч длиннее предела разбора claude.
	if os.Getenv("STUB_DENY_STAGE") == b.Stage {
		emit(m{"type": "assistant", "message": m{"id": "cdeny", "content": []m{{"type": "tool_use", "id": "deny", "name": "Bash", "input": m{"command": "review batch --json '[]'"}}}}})
		emit(m{"type": "system", "subtype": "permission_denied", "tool_name": "Bash", "tool_use_id": "deny"})
		emit(m{"type": "user", "message": m{"content": []m{{"type": "tool_result", "tool_use_id": "deny", "is_error": true, "content": "Permission to use Bash has been denied"}}}})
	}
	failed := 0
	for _, args := range plan(b) {
		out, status := review(args...)
		emit(m{"type": "assistant", "message": m{"id": "c" + args[0], "content": []m{{"type": "tool_use", "name": "Bash", "input": m{"command": "review " + args[0]}}}}})
		if status != 0 {
			failed++
		}
		emit(m{"type": "user", "message": m{"content": []m{{"type": "tool_result", "is_error": status != 0, "content": out}}}})
	}
	result := "готово"
	if failed > 0 {
		result = fmt.Sprintf("отказов харнесса: %d", failed)
	}
	emit(m{"type": "result", "is_error": failed > 0, "result": result, "total_cost_usd": 0.01,
		"modelUsage": m{"stub": m{"inputTokens": 1200, "outputTokens": 300, "cacheReadInputTokens": 4000, "cacheCreationInputTokens": 200}}})
	if failed > 0 {
		return 1
	}
	return 0
}

func codex(b brief, argv []string, prompt string) int {
	home, codexHome := os.Getenv("HOME"), os.Getenv("CODEX_HOME")
	_, authErr := os.Stat(filepath.Join(codexHome, "auth.json"))
	record(m{"driver": "codex", "stage": b.Stage, "argv": argv, "env": envNames(), "prompt": prompt, "home": home,
		"codexHome": codexHome, "auth": authErr == nil, "rules": readOr(filepath.Join(codexHome, "rules", "llamer.rules")),
		"skills": listDir(filepath.Join(home, ".agents", "skills")), "config": readOr(filepath.Join(codexHome, "config.toml"))})

	// Журнал сессии, как у codex: по записи на запрос; между ними вывод команды с токеном.
	session := os.Getenv("STUB_SESSION")
	if session == "" {
		session = strings.Join([]string{
			`{"type":"token_usage_record","payload":{"usage":{"input_tokens":2000,"cached_input_tokens":1500,"output_tokens":100}}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","output":"токен ` + Token + `"}}`,
			`{"type":"token_usage_record","payload":{"usage":{"input_tokens":3000,"cached_input_tokens":2500,"output_tokens":200,"reasoning_output_tokens":50}}}`,
			// Лимиты подписки — запись журнала сессии codex 0.166 дословно, без usage хода (`info`).
			`{"timestamp":"2026-11-06T10:57:40.928Z","ordinal":48,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":70.0,"window_minutes":10080,"resets_at":1794213888},"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":null},"individual_limit":null,"spend_control_reached":null,"plan_type":"self_serve_business_prolite","rate_limit_reached_type":null}}}`,
		}, "\n")
	}
	sessions := filepath.Join(codexHome, "sessions", "2026", "11", "05")
	must(os.MkdirAll(sessions, 0o777))
	must(os.WriteFile(filepath.Join(sessions, "rollout-stub.jsonl"), []byte(session), 0o666))

	emit(m{"type": "thread.started", "thread_id": "stub-" + b.Stage})
	emit(m{"type": "turn.started"})
	extraEvents()
	if os.Getenv("STUB_FAIL_STAGE") == b.Stage {
		emit(m{"type": "turn.failed", "error": m{"message": "Your workspace is out of credits."}})
		return 1
	}
	emit(m{"type": "item.completed", "item": m{"id": "m0", "type": "agent_message", "text": "заглушка этапа " + b.Stage}})
	failed := 0
	for i, args := range plan(b) {
		command := "review " + args[0] + " …"
		id := "c" + strconv.Itoa(i)
		emit(m{"type": "item.started", "item": m{"id": id, "type": "command_execution", "command": command}})
		out, status := review(args...)
		if status != 0 {
			failed++
		}
		emit(m{"type": "item.completed", "item": m{"id": id, "type": "command_execution", "command": command, "exit_code": status, "aggregated_output": out}})
	}
	if failed > 0 {
		emit(m{"type": "turn.failed", "error": m{"message": fmt.Sprintf("отказов харнесса: %d", failed)}})
		return 1
	}
	emit(m{"type": "turn.completed", "usage": m{"input_tokens": 5000, "cached_input_tokens": 4000, "output_tokens": 300}})
	return 0
}

var listed = regexp.MustCompile(`(?m)^(\d+)\. \[([^\]]+)\] ([^:]+):(\d+)`)

// plan — что заглушка делает на этапе. Суждения о коде в ней нет: находки
// заданы заранее, но пишутся теми же командами харнесса, что у модели, и
// проходят те же проверки. Журнал прогона она читает через `review list`.
func plan(b brief) [][]string {
	switch b.Stage {
	case "assess":
		return [][]string{{"assess", "--nature", "behavioral", "--risk", "medium", "--level", "2", "--reason", "заглушка: вердикт задан заранее"}}
	case "threads":
		var commands [][]string
		for _, t := range b.Threads {
			commands = append(commands, []string{"resolve", "--id", fmt.Sprint(t["id"]), "--reason", "заглушка: замечание снято правкой, токен " + Token})
		}
		return commands
	case "dedupe":
		kept := map[string]string{}
		var commands [][]string
		for _, f := range listed.FindAllStringSubmatch(output("list"), -1) {
			place := f[3] + ":" + f[4]
			if of, ok := kept[place]; ok {
				commands = append(commands, []string{"dupe", "--id", f[1], "--of", of, "--reason", "заглушка: то же место"})
			} else {
				kept[place] = f[1]
			}
		}
		return commands
	case "skeptic":
		refuted := strconv.FormatBool(os.Getenv("STUB_SKEPTIC_REFUTES") != "")
		var commands [][]string
		for _, f := range listed.FindAllStringSubmatch(output("list"), -1) {
			if !strings.HasPrefix(f[2], "повтор") {
				commands = append(commands, []string{"verdict", "--id", f[1], "--refuted", refuted, "--reason", "заглушка решила, токен " + Token})
			}
		}
		return commands
	}
	return analysis(b)
}

// analysis — находка на первой изменённой строке первого файла и сводка, если она у этапа есть.
func analysis(b brief) [][]string {
	if os.Getenv("STUB_STOP_STAGE") == b.Stage {
		return [][]string{{"stop", "--reason", "заглушка: часть кода не разобрана"}}
	}
	var commands [][]string
	if file, line := firstAdded(b.DiffPath); file != "" {
		at := strconv.Itoa(line)
		commands = append(commands, []string{"finding", "--file", file, "--line", at, "--line-end", at,
			"--text", "заглушка этапа " + b.Stage + " отметила " + file + ":" + at + ".\nВторая строка: токен " + Token,
			"--severity", "major", "--severity-reason", "заглушка ставит вес", "--related", file + ":" + at})
	}
	if slices.Contains(b.Commands, "summary") && os.Getenv("STUB_NO_SUMMARY") != b.Stage {
		commands = append(commands, []string{"summary", "--text", "заглушка этапа " + b.Stage + ": разбора кода не было"})
	}
	return commands
}

var hunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// firstAdded — первая добавленная строка диффа задачи: файл и её номер.
func firstAdded(diffPath string) (string, int) {
	f, err := os.Open(diffPath)
	must(err)
	defer f.Close()
	file, line := "", 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		text := scanner.Text()
		switch {
		case strings.HasPrefix(text, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(text, "+++ "), "b/")
		case hunk.MatchString(text):
			line, _ = strconv.Atoi(hunk.FindStringSubmatch(text)[1])
		case strings.HasPrefix(text, "+") && file != "/dev/null":
			return file, line
		case strings.HasPrefix(text, " "):
			line++
		}
	}
	return "", 0
}
