package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// workflowProject — проект с workflow из двух этапов: writer отвечает по схеме
// writerAnswer (заглушка отдаёт строку `ANSWER:` задания), checker отвечает его ответом.
func workflowProject(t *testing.T, writerPrompt string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, ".llamer.json", `{"version": 2, "drivers": {"claude": {}}}`)
	write(t, root, "wf/workflow.json", `{"version": 1, "stages": [
		{"name": "writer", "driver": "claude", "model": "m", "effort": "low", "prompt": "writer.md", "timeout": "1m", "schema": "joke.schema.json"},
		{"name": "checker", "driver": "claude", "model": "m", "effort": "low", "prompt": "checker.md", "timeout": "1m"}]}`)
	write(t, root, "wf/joke.schema.json", `{"type": "object", "additionalProperties": false, "required": ["joke"],
		"properties": {"joke": {"type": "string"}}}`)
	write(t, root, "wf/writer.md", writerPrompt)
	write(t, root, "wf/checker.md", "ANSWER: проверено {{.Answers.writer}}\n")
	return root
}

var runWorkflowArgs = []string{"run", "wf", "--config", ".llamer.json"}

type workflowResult struct {
	OK      bool `json:"ok"`
	Failure *struct {
		Stage, Reason string
	} `json:"failure"`
	Stages []struct {
		Name   string          `json:"name"`
		Answer json.RawMessage `json:"answer"`
	} `json:"stages"`
}

// lines — строки JSON вывода llamer run по типу.
func lines(t *testing.T, stdout string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("строка вывода не JSON: %v\n%s", err, line)
		}
		out[v["type"].(string)] = append(out[v["type"].(string)], v)
	}
	return out
}

func lastResult(t *testing.T, stdout string) workflowResult {
	t.Helper()
	all := strings.Split(strings.TrimSpace(stdout), "\n")
	var r workflowResult
	if err := json.Unmarshal([]byte(all[len(all)-1]), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Секрет в ответе writer: в ответе checker он вложен глубже, чем в ответе writer.
const workflowSecret = `wf-leak-value-1234`

func TestWorkflowAnswerReachesNextStage(t *testing.T) {
	t.Parallel()
	root := workflowProject(t, "ANSWER: {\"joke\": \"кот шёл по коридору wf-leak-value-1234\"}\n")
	r := run(t, root, runWorkflowArgs, map[string]string{"WORKFLOW_TOKEN": workflowSecret})
	equal(t, r.status, 0, "код\n"+r.stderr)
	res := lastResult(t, r.stdout)
	equal(t, res.OK, true, "ok")
	equal(t, string(res.Stages[0].Answer), `{"joke":"кот шёл по коридору ***REDACTED***"}`, "ответ writer — JSON по схеме")
	equal(t, string(res.Stages[1].Answer), `"проверено {\"joke\": \"кот шёл по коридору ***REDACTED***\"}"`, "ответ checker без схемы — строка")
	equal(t, len(r.launches), 2, "оба этапа запускались")
	match(t, r.launches[1]["prompt"].(string), `проверено \{"joke": "кот шёл по коридору wf-leak-value-1234"\}`, "checker получил ответ writer как есть")
	equal(t, slices.Contains(argvOf(r.launches[0]), "--json-schema"), true, "схема ушла драйверу writer")
	equal(t, slices.Contains(argvOf(r.launches[1]), "--json-schema"), false, "у checker схемы нет")
	events := lines(t, r.stdout)["event"]
	equal(t, events[0]["stage"], any("writer"), "событие этапа названо этапом")
	_, kind := events[0]["event"].(map[string]any)["kind"]
	equal(t, kind, true, "событие — в форме LLaMer")
	runDir := lines(t, r.stdout)["result"][0]["run"].(string)
	match(t, read(t, filepath.Join(runDir, "writer.raw.jsonl")), `(?m)^\{.*"type":"result"\}$`, "вывод CLI этапа — как есть")
	data := read(t, filepath.Join(runDir, "result.json"))
	equal(t, strings.TrimSpace(data), strings.TrimSpace(r.stdout[strings.LastIndex(strings.TrimSpace(r.stdout), "\n")+1:]), "result.json — последняя строка вывода")
	noMatch(t, r.stdout+r.stderr, `leak-value`, "секрет в выводе")
	filepath.WalkDir(lines(t, r.stdout)["result"][0]["run"].(string), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			noMatch(t, read(t, path), `leak-value`, "секрет в артефакте "+path)
		}
		return err
	})
}

func TestWorkflowResultNotSavedFailsRun(t *testing.T) {
	t.Parallel()
	r := run(t, workflowProject(t, "STUB_BLOCK_RESULT\nANSWER: {\"joke\": \"кот\"}\n"), runWorkflowArgs, nil)
	equal(t, r.status, 1, "код\n"+r.stderr)
	noMatch(t, r.stdout, `(?m)^\{"type":"result"`, "результата в выводе нет")
}

func TestWorkflowAnswerOffSchemaFailsRun(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{"не по схеме": `{"joke": 5}`, "не JSON": "кот"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := run(t, workflowProject(t, "ANSWER: "+answer+"\n"), runWorkflowArgs, nil)
			equal(t, r.status, 1, "код\n"+r.stderr)
			res := lastResult(t, r.stdout)
			equal(t, res.OK, false, "ok")
			equal(t, res.Failure.Stage+" "+res.Failure.Reason, "writer schema", "сорван writer по схеме")
			equal(t, string(res.Stages[0].Answer), "null", "ответа нет")
			equal(t, len(r.launches), 1, "checker не запускался")
		})
	}
}

func TestWorkflowSigterm(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root := workflowProject(t, "STUB_HANG\n")
	done := make(chan *result)
	go func() { done <- run(t, root, runWorkflowArgs, map[string]string{"TMPDIR": tmp}) }()
	hang := filepath.Join(tmp, "hang.json")
	for wait := 0; wait < 500; wait++ {
		if _, err := os.Stat(hang); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var pids struct{ Llamer, Stage, Command int }
	if err := json.Unmarshal([]byte(read(t, hang)), &pids); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pids.Llamer, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	r := <-done

	equal(t, r.status, 143, "код\n"+r.stderr)
	noMatch(t, r.stdout, `"type":"result"`, "результата нет")
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	// Убитые процессы ещё мгновение висят зомби, пока их не приберёт init.
	for wait := 0; wait < 50 && (alive(pids.Stage) || alive(pids.Command)); wait++ {
		time.Sleep(20 * time.Millisecond)
	}
	equal(t, alive(pids.Stage) || alive(pids.Command), false, "этап и его команда остановлены")
	leftovers, _ := filepath.Glob(filepath.Join(tmp, "llamer-run-*"))
	equal(t, len(leftovers), 0, "временное прогона убрано")
}

// Песочница этапа llamer run доходит до аргументов обоих драйверов: каталог контекста и
// путь из `readable` workflow открыты на чтение, остальное закрыто; CODEX_HOME — общий с
// auth.json пользователя, а секреты окружения llamer агенту не передаются.
func TestWorkflowSandboxReachesDrivers(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, ".llamer.json", `{"version": 2, "drivers": {"claude": {}, "codex": {}}}`)
	write(t, root, "extra/notes.md", "заметки")
	write(t, root, "wf/workflow.json", `{"version": 1, "readable": ["../extra"], "stages": [
		{"name": "writer", "driver": "claude", "model": "m", "effort": "low", "prompt": "writer.md", "timeout": "1m"},
		{"name": "checker", "driver": "codex", "model": "m", "effort": "low", "prompt": "checker.md", "timeout": "1m"}]}`)
	write(t, root, "wf/writer.md", "ANSWER: кот\n")
	write(t, root, "wf/checker.md", "ANSWER: проверено {{.Answers.writer}}\n")
	extra := filepath.Join(root, "extra")

	r := run(t, root, runWorkflowArgs, map[string]string{"WORKFLOW_TOKEN": workflowSecret})

	equal(t, r.status, 0, "код\n"+r.stderr)
	equal(t, len(r.launches), 2, "оба этапа запускались")
	claude, codex := argvOf(r.launches[0]), argvOf(r.launches[1])
	var settings struct {
		Sandbox struct {
			Enabled    bool
			Filesystem struct{ DenyRead, AllowRead []string }
		}
	}
	if err := json.Unmarshal([]byte(claude[slices.Index(claude, "--settings")+1]), &settings); err != nil {
		t.Fatal(err)
	}
	equal(t, settings.Sandbox.Enabled, true, "песочница claude включена")
	equal(t, slices.Equal(settings.Sandbox.Filesystem.DenyRead, []string{"/"}), true, "claude: чтение закрыто")
	equal(t, slices.Contains(settings.Sandbox.Filesystem.AllowRead, root) && slices.Contains(settings.Sandbox.Filesystem.AllowRead, extra), true,
		"claude: контекст и readable открыты команде")
	equal(t, slices.Contains(claude, extra), true, "claude: readable открыт инструментам (--add-dir)")
	equal(t, slices.Contains(strings.Fields(claude[slices.Index(claude, "--allowedTools")+1]), "Read"), false,
		"claude: голый Read открыл бы любой путь")
	match(t, strings.Join(codex, "\n"), `permissions\.llamer\.filesystem=\{":root" = "deny", ":minimal" = "read", "`+regexp.QuoteMeta(root)+`" = "read", "`+
		regexp.QuoteMeta(extra)+`" = "read"`, "codex: чтение закрыто, контекст и readable открыты")
	equal(t, r.launches[1]["codexHome"], any(r.env["CODEX_HOME"]), "codex: CODEX_HOME общий")
	for _, l := range r.launches {
		env := strs(l["env"])
		equal(t, slices.Contains(env, "WORKFLOW_TOKEN") || slices.Contains(env, "GITLAB_TOKEN"), false, "секрет в окружении агента")
	}
}
