package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func match(t *testing.T, text, pattern, why string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%s: нет /%s/ в\n%s", why, pattern, text)
	}
}

func noMatch(t *testing.T, text, pattern, why string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%s: есть /%s/ в\n%s", why, pattern, text)
	}
}

func equal[T comparable](t *testing.T, got, want T, why string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: %v, а ждали %v", why, got, want)
	}
}

func sameList[T comparable](t *testing.T, got, want []T, why string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %v, а ждали %v", why, got, want)
	}
}

func ok(t *testing.T, r *result) {
	t.Helper()
	if r.status != 0 {
		t.Fatalf("код %d\n%s", r.status, r.stderr)
	}
}

func strs(v any) []string {
	var out []string
	for _, item := range v.([]any) {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func argAfter(argv []string, flag string) string { return argv[slices.Index(argv, flag)+1] }

func argvOf(l obj) []string { return strs(l["argv"]) }

// Поля результата ревью: одни и те же, разбирал ли прогон что-то или нет.
var resultKeys = []string{"version", "input", "head", "ok", "base", "assessment", "findings", "summaries",
	"resolvedThreads", "gaps", "notices", "cost", "limits", "stages", "report", "result", "artifacts"}

func keysOf(t *testing.T, stdout string) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for i, node := range doc.Content[0].Content {
		if i%2 == 0 {
			keys = append(keys, node.Value)
		}
	}
	return keys
}

func TestWorktreeRun(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	before := git(t, root, "status", "--porcelain")

	r := run(t, root, worktree, nil)

	ok(t, r)
	equal(t, r.answer["version"], any(3), "версия результата")
	equal(t, r.answer["ok"], any(true), "ok")
	equal(t, r.str("input"), "revisions", "вход")
	equal(t, r.answer["assessment"].(obj)["level"], any(2), "уровень оценщика")
	// Два разбирающих этапа отметили одно место — дедупликация оставила одну находку.
	findings := r.list("findings")
	equal(t, len(findings), 1, "находок")
	var summaries []string
	for _, s := range r.list("summaries") {
		summaries = append(summaries, s.(obj)["stage"].(string))
	}
	sameList(t, summaries, []string{"logic"}, "сводки")
	report := read(t, r.str("report"))
	match(t, report, `## Находки: 1`, "report.md")
	// Тот же документ — в result.yaml; многострочный текст — блоком.
	equal(t, read(t, r.str("result")), r.stdout, "result.yaml")
	match(t, r.stdout, `(?m)^ {4}text: \|-?\n {6}заглушка этапа \S+ отметила src/Fresh\.php:1\.\n {6}Вторая строка: токен`, "текст находки блоком")
	sameList(t, keysOf(t, r.stdout), resultKeys, "поля результата")
	f := findings[0].(obj)
	equal(t, fmt.Sprintf("%v %v %v %v", f["file"], f["line"], f["deleted"], f["severity"]), "src/Fresh.php 1 false major", "находка")
	equal(t, len(r.list("stages")), len(r.launches), "запись на каждый поднятый этап")
	for _, s := range r.list("stages") {
		if s.(obj)["name"] == "logic" {
			match(t, s.(obj)["resume"].(string), `claude -p --resume stub-logic`, "resume logic")
		}
	}

	// Токен, напечатанный заглушкой в находке, не доходит ни до отчёта, ни до журналов.
	match(t, f["text"].(string), `токен \*\*\*REDACTED\*\*\*`, "токен в находке")
	written := []string{r.stdout, report}
	journals := filepath.Join(r.str("artifacts"), "journal")
	entries, _ := os.ReadDir(journals)
	for _, e := range entries {
		written = append(written, read(t, filepath.Join(journals, e.Name())))
	}
	for _, text := range written {
		if strings.Contains(text, token) {
			t.Errorf("токен в отчёте или журнале:\n%s", text)
		}
	}

	// Этапы docs и tests не поднялись: файлов под их маски в изменениях нет. Тредов локально нет.
	sameList(t, r.stages(), []string{"assess", "comments", "dedupe", "logic", "quality", "skeptic"}, "поднятые этапы")
	equal(t, git(t, root, "status", "--porcelain"), before, "индекс и рабочее дерево разработчика не тронуты")
}

func TestWorktreeExcludesForeignChanges(t *testing.T) {
	t.Parallel()
	r := run(t, makeRepo(t, config()), worktree, nil)

	diff := read(t, filepath.Join(r.str("artifacts"), "task.diff"))
	match(t, diff, `\+\s+return 3;`, "незакоммиченная правка в разборе")
	match(t, diff, `b/src/Fresh\.php`, "неотслеживаемый файл в разборе")
	noMatch(t, diff, `Foreign\.php`, "правка целевой ветки после ответвления не в разборе")
}

func TestRefutedFindingRedactedInLog(t *testing.T) {
	t.Parallel()
	r := run(t, makeRepo(t, with(config(), "drivers", stubEnvOf("STUB_SKEPTIC_REFUTES", "1"))), worktree, nil)

	ok(t, r)
	equal(t, len(r.list("findings")), 0, "находок")
	match(t, r.stderr, `скептик опроверг находок: 1`, "лог")
	match(t, r.stderr, `токен \*\*\*REDACTED\*\*\*`, "лог")
	if strings.Contains(r.stderr, token) {
		t.Error("токен в stderr")
	}
}

func TestWorktreeSnapshotSeesRacyEdit(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	// У файла и индекса одно время: по времени и размеру файл неотличим от
	// записанного в индекс, и правку видит только перечитывание «гоночных» записей.
	// ctime не выставить — отключаем его сверку через core.trustctime.
	file := filepath.Join(root, "src/Order.php")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	git(t, root, "config", "core.trustctime", "false")
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "src/Order.php")
	write(t, root, "src/Order.php", "<?php\n\nfunction total() {\n    return 5;\n}\n")
	for _, path := range []string{file, filepath.Join(root, ".git/index")} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}

	r := run(t, root, worktree, nil)

	ok(t, r)
	match(t, read(t, filepath.Join(r.str("artifacts"), "task.diff")), `\+\s+return 5;`, "правка в снимке")
}

func TestIndexHead(t *testing.T) {
	t.Parallel()
	nothing := run(t, makeRepo(t, config()), []string{"--base", "HEAD", "--head", "index"}, nil)
	ok(t, nothing)
	match(t, nothing.stderr, `разбирать нечего`, "лог")
	match(t, read(t, nothing.str("report")), `Разбирать нечего`, "report.md")
	equal(t, len(nothing.launches), 0, "запусков")

	root := makeRepo(t, config())
	git(t, root, "add", "src/Fresh.php")
	index := read(t, filepath.Join(root, ".git/index"))
	status := git(t, root, "status", "--porcelain")

	r := run(t, root, []string{"--base", "HEAD", "--head", "index"}, nil)

	ok(t, r)
	diff := read(t, filepath.Join(r.str("artifacts"), "task.diff"))
	match(t, diff, `b/src/Fresh\.php`, "staged-файл в разборе")
	noMatch(t, diff, `return 3;`, "незастейдженная правка не в разборе")
	equal(t, read(t, filepath.Join(root, ".git/index")), index, "файл индекса не переписан")
	equal(t, git(t, root, "status", "--porcelain"), status, "статус")
}

func TestTaskText(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	write(t, root, ".git/task.md", "Итог заказа — три.\n")

	r := run(t, root, append(slices.Clone(worktree), "--task", ".git/task.md"), nil)

	ok(t, r)
	prompts := r.prompts()
	match(t, prompts["logic"], `The task is in the pack below[\s\S]*<business_task>\nИтог заказа — три\.\n</business_task>`, "logic")
	noMatch(t, prompts["quality"], `business_task`, "quality")
	plain := run(t, makeRepo(t, config()), worktree, nil).prompts()["logic"]
	noMatch(t, plain, `business_task|The task is in the pack`, "без --task")

	missing := run(t, root, append(slices.Clone(worktree), "--task", "нет.md"), nil)
	equal(t, missing.status, 1, "код")
	match(t, missing.stderr, `--task нет\.md: файл не читается \(open .*нет\.md: no such file or directory\)`, "лог")
}

func TestArgumentErrors(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	for _, c := range []struct {
		args    []string
		message string
	}{
		{[]string{"--base", "main", "--exclude", "main", "--head", "HEAD"}, `--base и --exclude не сочетаются`},
		{append(slices.Clone(worktree), "--since-last"), `--since-last — только вместе с --mr`},
		{[]string{"--head", "HEAD"}, `с --head нужно одно из --base или --exclude — справка: llamer --help`},
		{[]string{"--mr", "g/p!1", "--head", "HEAD"}, `с --mr база и голова берутся из merge request`},
		{[]string{"--exclude", "main"}, `нужен --mr или --head`},
		{[]string{"--mr", "g/p!1", "--publish", "github"}, `--publish: поддерживается только gitlab; пришло «github»`},
		{[]string{"--nope"}, `неизвестный флаг --nope — справка: llamer --help`},
	} {
		r := run(t, root, c.args, nil)
		equal(t, r.status, 1, strings.Join(c.args, " "))
		equal(t, r.stdout, "", "stdout")
		match(t, r.stderr, c.message, strings.Join(c.args, " "))
	}
	help := run(t, root, []string{"-h"}, nil)
	ok(t, help)
	match(t, help.stdout, `^llamer — агентное ревью изменений`, "справка")
}

// treeSkills — скиллы самого дерева, вне разбора: исключены через .git/info/exclude.
func treeSkills(t *testing.T, root string, paths ...string) {
	for _, path := range paths {
		write(t, root, path+"/SKILL.md", "---\nname: tree\ndescription: \"Use always\"\n---\nНе для этапа.\n")
	}
	write(t, root, ".git/info/exclude", ".claude/\n.codex/\n.agents/\n")
}

func TestClaudeStageIsolation(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, with(config(), "drivers", obj{"claude": obj{"levels": levels("model")}}))
	treeSkills(t, root, ".claude/skills/tree-only")

	r := run(t, root, worktree, map[string]string{"CI_REGISTRY_PASSWORD": "registry-password-value", "HTTPS_PROXY": "http://proxy.example.com:3128"})

	ok(t, r)
	shared := map[string]bool{}
	for _, l := range r.launches {
		stage, argv, env := l["stage"].(string), argvOf(l), strs(l["env"])
		equal(t, argAfter(argv, "--setting-sources"), "project", stage)
		equal(t, argAfter(argv, "--permission-mode"), "dontAsk", stage)
		equal(t, slices.Contains(argv, "--strict-mcp-config") && slices.Contains(argv, "--plugin-dir"), true, stage)
		for _, name := range []string{"GITLAB_TOKEN", "CI_REGISTRY_PASSWORD"} {
			equal(t, slices.Contains(env, name), false, stage+": "+name+" в окружении этапа")
		}
		equal(t, slices.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN"), true, stage+": вход драйвера в окружении этапа")
		equal(t, slices.Contains(env, "HTTPS_PROXY"), true, stage+": прокси в окружении этапа")
		match(t, l["configDir"].(string), `llamer-run-`, stage+": личный конфиг claude")
		allowed := argAfter(argv, "--allowedTools")
		equal(t, strings.Contains(allowed, "Bash(rg *)"), true, stage+": общий allow")
		equal(t, strings.Contains(allowed, "Bash(git log *)"), stage == "logic", stage+": allow этапа только у него")
		equal(t, strings.Contains(allowed, "Bash(review) Bash(review *)"), true, stage+": харнесс разрешён целиком")
		var settings struct{ SkillOverrides map[string]string }
		if err := json.Unmarshal([]byte(argAfter(argv, "--settings")), &settings); err != nil {
			t.Fatal(err)
		}
		equal(t, fmt.Sprint(settings.SkillOverrides), "map[tree-only:off]", stage+": скилл .claude/skills дерева выключен")
		if pack, ok := l["shared"].(string); ok {
			shared[pack] = true
		}
	}
	sameList(t, strs(r.launch(t, "logic")["skills"]), []string{"backend", "reviewing", "reviewing-business-logic"}, "плагин этапа — его always и onDemand")
	sameList(t, strs(r.launch(t, "assess")["skills"]), []string{"assessing-review-scope"}, "чужих скиллов прогона у этапа нет")
	equal(t, len(shared), 1, "общая часть пакета одна на все этапы — иначе кэш не поделится")
	var pack string
	for p := range shared {
		pack = p
	}
	match(t, pack, `<request_materials>`, "пакет")
	// Голова локального прогона — снимок рабочего дерева: незакоммиченная правка в выводе, как в пакете.
	diff := r.launch(t, "logic")["diff"].(string)
	match(t, diff, `^diff --git a/src/Order\.php b/src/Order\.php\n[\s\S]*\n {4}4 \+    return 3;\n`, "review diff")
	equal(t, strings.Contains(pack, strings.TrimRight(diff, "\n")), true, "review diff — та же секция, что в пакете")

	modelOf := func(stage string) string {
		argv := argvOf(r.launch(t, stage))
		return argAfter(argv, "--model") + "/" + argAfter(argv, "--effort")
	}
	equal(t, modelOf("assess"), "model-1/effort-1", "оценщик — уровень 1")
	equal(t, modelOf("logic"), "model-2/effort-2", "logic идёт на уровне оценщика (2)")
	equal(t, slices.Contains(argvOf(r.launch(t, "logic")), "--append-system-prompt-file"), true, "logic получает пакет")
	equal(t, slices.Contains(argvOf(r.launch(t, "dedupe")), "--append-system-prompt-file"), false, "дедупликации пакет не нужен")

	logic := r.launch(t, "logic")["prompt"].(string)
	match(t, logic, "## Your Method\n\nLoad each of these with the Skill tool before you start. They are your method,\nthey are not optional, and nothing below repeats what is in them.\n\n- stage:reviewing-business-logic\n- stage:reviewing\n", "always: встроенный и добавленный конфигом")
	noMatch(t, logic, `backend`, "onDemand в задании не называется")

	// Сессия этапа — в артефактах без кредов и с вымаранным токеном; команда продолжения — в отчёте.
	session := filepath.Join(r.str("artifacts"), "sessions", "logic")
	var files []string
	_ = filepath.WalkDir(session, func(path string, d os.DirEntry, err error) error {
		if path != session {
			rel, _ := filepath.Rel(session, path)
			files = append(files, rel)
		}
		return nil
	})
	sameList(t, files, []string{"projects", "projects/-stub-tree", "projects/-stub-tree/stub-logic.jsonl"}, "сессия")
	match(t, read(t, filepath.Join(session, "projects/-stub-tree/stub-logic.jsonl")), `токен \*\*\*REDACTED\*\*\*`, "сессия")
	resume := "CLAUDE_CONFIG_DIR=" + session + " claude -p --resume stub-logic --model model-2 --effort effort-2 '<вопрос>'"
	equal(t, strings.Contains(read(t, r.str("report")), "- logic: `"+resume+"`"), true, "команда продолжения в report.md")
	equal(t, strings.Contains(r.stderr, "продолжить сессию «logic» вопросом: "+resume), true, "команда продолжения в логе")
}

func TestCodexDriver(t *testing.T) {
	t.Parallel()
	all := obj{}
	for key, stage := range config()["stages"].(obj) {
		s := stage.(obj)
		s["drivers"] = []string{"codex"}
		all[key] = s
	}
	var ladder []obj
	for _, l := range levels("model") {
		ladder = append(ladder, obj{"model": "gpt-6.1-terra", "effort": l["effort"]})
	}
	root := makeRepo(t, with(config(), "drivers", obj{"codex": obj{"levels": ladder}}, "stages", all))
	treeSkills(t, root, ".codex/skills/tree-codex", ".agents/skills/group/tree-agents")

	r := run(t, root, worktree, nil)

	ok(t, r)
	equal(t, len(r.list("findings")), 1, "находок")
	sameList(t, r.stages(), []string{"assess", "comments", "dedupe", "logic", "quality", "skeptic"}, "поднятые этапы")
	for _, l := range r.launches {
		stage, argv := l["stage"].(string), argvOf(l)
		equal(t, l["driver"], any("codex"), stage)
		sameList(t, argv[:4], []string{"exec", "--json", "--sandbox", "read-only"}, stage)
		equal(t, slices.Contains(argv, "--ignore-user-config"), false, stage+": config.toml этапа — свой, его надо читать")
		equal(t, slices.Contains(argv, "skills.bundled.enabled=false"), true, stage+": встроенные скиллы codex отключены")
		cfg := l["config"].(string)
		match(t, strings.Split(cfg, "\n[[")[0], `(?m)^allow_login_shell = false$`, stage+": команды — без login-шелла, до таблиц config.toml")
		equal(t, l["home"] != r.env["HOME"] && l["codexHome"] != r.env["CODEX_HOME"], true, stage+": личные HOME и CODEX_HOME")
		equal(t, l["auth"], any(true), stage+": вход codex скопирован")
		rules := l["rules"].(string)
		match(t, rules, `prefix_rule\(pattern=\["review"\], decision="allow"\)`, stage)
		match(t, rules, `pattern=\["rg"\]`, stage)
		equal(t, strings.Contains(rules, `pattern=["git","log"]`), stage == "logic", stage+": allow этапа")
		equal(t, slices.Contains(strs(l["env"]), "GITLAB_TOKEN"), false, stage+": токен GitLab в окружении")
	}
	logic, dedupe := r.launch(t, "logic"), r.launch(t, "dedupe")
	match(t, logic["config"].(string), `^developer_instructions = "What follows is the material of a merge request`, "общий пакет — developer_instructions")
	match(t, logic["prompt"].(string), `^You are a Senior Software Engineer`, "в задании пакета нет")
	noMatch(t, dedupe["config"].(string), `developer_instructions`, "дедупликации пакет не нужен")
	for _, path := range []string{".codex/skills/tree-codex", ".agents/skills/group/tree-agents"} {
		match(t, dedupe["config"].(string), `\[\[skills\.config\]\]\npath = '`+regexp.QuoteMeta(root+"/"+path)+`/SKILL\.md'\nenabled = false\n`, path+": скилл дерева выключен")
	}
	sameList(t, strs(r.launch(t, "assess")["skills"]), []string{"assessing-review-scope"}, "чужих скиллов прогона у этапа нет")
	prompt := logic["prompt"].(string)
	equal(t, strings.Contains(prompt, "closed as `values['\"'\"'severity'\"'\"']`"), true, "кавычка — в форме, которую признают правила codex")
	match(t, prompt, "Each of these is attached by its mention below — its text is already with you, follow it from the start. They are your method,\nthey are not optional, and nothing below repeats what is in them.\n\n- \\$reviewing-business-logic\n- \\$reviewing\n", "always — упоминанием $имя")
	noMatch(t, prompt, `backend`, "onDemand в задании не называется")
	for _, name := range []string{"reviewing", "backend"} {
		equal(t, slices.Contains(strs(logic["skills"]), name), true, name+": скилл в $HOME/.agents/skills")
	}

	// Цена и пик контекста — по запросам из журнала сессии заглушки: 2000 и 3000 токенов входа.
	var cost struct {
		Stages []struct {
			Name          string
			ContextTokens float64
			Cost          float64
			Requests      []map[string]float64
		}
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(r.str("artifacts"), "cost.json"))), &cost); err != nil {
		t.Fatal(err)
	}
	for _, s := range cost.Stages {
		equal(t, s.ContextTokens, 3000, s.Name+": пик контекста")
		equal(t, math.Abs(s.Cost-0.0027) < 1e-9, true, fmt.Sprintf("%s: цена %v", s.Name, s.Cost))
		equal(t, fmt.Sprint(s.Requests), "[map[cached_input_tokens:1500 input_tokens:2000 output_tokens:100] map[cached_input_tokens:2500 input_tokens:3000 output_tokens:200 reasoning_output_tokens:50]]", s.Name+": запросы из журнала сессии")
	}

	// Журнал сессии этапа — в артефактах: без auth.json, с вымаранным токеном; продолжение — от обычного входа.
	session := filepath.Join(r.str("artifacts"), "sessions", "logic")
	entries, _ := os.ReadDir(session)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sameList(t, names, []string{"config.toml", "rules", "sessions"}, "сессия")
	match(t, read(t, filepath.Join(session, "sessions/2026/11/05/rollout-stub.jsonl")), `токен \*\*\*REDACTED\*\*\*`, "сессия")
	auth := filepath.Join(r.env["CODEX_HOME"], "auth.json")
	resume := "ln -sf " + auth + " " + session + "/auth.json && CODEX_HOME=" + session + " codex exec resume --skip-git-repo-check" +
		` -c 'model="gpt-6.1-terra"' -c 'model_reasoning_effort="effort-2"' stub-logic '<вопрос>'`
	equal(t, strings.Contains(read(t, r.str("report")), "- logic: `"+resume+"`"), true, "команда продолжения в report.md")
}

func TestStageLosingWorkFails(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ knob, stage, reason string }{
		{"STUB_NO_SUMMARY", "logic", "сводка не записана"},
		{"STUB_DENY_STAGE", "quality", "вызовов харнесса не дошло до журнала: 1"},
	} {
		r := run(t, makeRepo(t, with(config(), "drivers", stubEnvOf(c.knob, c.stage))), worktree, nil)

		equal(t, r.status, 1, c.knob)
		equal(t, slices.Contains(strs(r.answer["gaps"]), "этап «"+c.stage+"» сорвался: "+c.reason), true, c.knob+": недостача в результате")
		report := read(t, r.str("report"))
		equal(t, strings.Contains(report, "## Недостача\n\n- этап «"+c.stage+"» сорвался: "+c.reason), true, c.knob+": недостача в отчёте")
		equal(t, strings.Contains(report, c.stage+" ("+c.reason+")"), true, c.knob+": этап помечен в таблице цены")
	}
}

// Записи харнесса, не дошедшие до журнала: отказ харнесса агент чинит повтором,
// справка и чтение записи не несут, запись после чтения в той же команде — запись.
func TestLostHarnessWrites(t *testing.T) {
	t.Parallel()
	call := func(id, command string) string {
		data, _ := json.Marshal(obj{"type": "assistant", "message": obj{"content": []obj{{"type": "tool_use", "id": id, "name": "Bash", "input": obj{"command": command}}}}})
		return string(data)
	}
	answer := func(id, content string) string {
		data, _ := json.Marshal(obj{"type": "user", "message": obj{"content": []obj{{"type": "tool_result", "tool_use_id": id, "is_error": true, "content": content}}}})
		return string(data)
	}
	claude := strings.Join([]string{
		call("t1", "review batch --json '[\n{}\n]'"), answer("t1", "Permission to use Bash has been denied because Claude Code is running in don't ask mode."),
		call("t2", "review finding --file a --line 1 --text 'x'"), answer("t2", "Exit code 1\nотказ: строка 1 в a не изменена"),
		call("t3", "rg missing"), answer("t3", "Exit code 1"),
		call("t4", "review --help; lsd def X"), answer("t4", "Exit code 5"),
		call("t5", "review diff a.php"), answer("t5", "Exit code 1"),
		call("t6", "review list && review finding --file a --line 1 --text 'x'"), answer("t6", "Permission to use Bash has been denied"),
	}, "\n")
	r := run(t, makeRepo(t, with(config(), "drivers", obj{"claude": obj{"env": obj{"STUB_EVENTS": claude}}})), worktree, nil)
	equal(t, r.status, 1, "код")
	equal(t, slices.Contains(strs(r.answer["gaps"]), "этап «оценщик» сорвался: вызовов харнесса не дошло до журнала: 2") ||
		strings.Contains(r.stderr, "вызовов харнесса не дошло до журнала: 2"), true, "claude: потеряно две записи")
	match(t, r.stderr, `\[оценщик\] отказов: 6`, "claude: отказов")

	codex := `{"type":"item.started","item":{"id":"x1","type":"command_execution","command":"/bin/bash -lc 'review batch --json []'"}}` + "\n" +
		`{"type":"item.completed","item":{"id":"x1","type":"command_execution","exit_code":2,"aggregated_output":"сбой харнесса: EROFS: read-only file system"}}`
	all := stages(nil, map[string][]string{"all": {"codex"}})
	r = run(t, makeRepo(t, with(config(), "stages", all, "drivers", obj{"codex": obj{"env": obj{"STUB_EVENTS": codex}}})), worktree, nil)
	equal(t, r.status, 1, "код")
	match(t, r.stderr, `этап «оценщик» сорван: вызовов харнесса не дошло до журнала: 1`, "codex: потеряна запись")
}

func TestRevisionsHeadInSeparateTree(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "finish")
	head := git(t, root, "rev-parse", "HEAD")
	base := git(t, root, "merge-base", "main", "HEAD")
	git(t, root, "checkout", "-q", "main")

	r := run(t, root, []string{"--base", base, "--head", head}, nil)

	ok(t, r)
	equal(t, r.str("input"), "revisions", "вход")
	equal(t, r.str("base"), base, "база")
	var files []string
	for _, f := range r.list("findings") {
		files = append(files, f.(obj)["file"].(string))
	}
	sameList(t, files, []string{"src/Fresh.php"}, "находки")
	match(t, r.stderr, `выложена в`, "лог")
	equal(t, len(strings.Split(git(t, root, "worktree", "list"), "\n")), 1, "временное дерево убрано")
	equal(t, git(t, root, "rev-parse", "--abbrev-ref", "HEAD"), "main", "ветка разработчика")
}

func gapIDs(r *result) []string {
	var ids []string
	for _, g := range r.list("gaps") {
		ids = append(ids, g.(obj)["id"].(string))
	}
	slices.Sort(ids)
	return ids
}

func TestPreflight(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg["stages"].(obj)["extra"] = obj{"skills": obj{"always": []string{"missing-skill"}}, "task": "Что-то ещё.", "drivers": []string{"claude"}}
	root := makeRepo(t, cfg)

	preflight := run(t, root, append(slices.Clone(worktree), "--preflight"), nil)
	equal(t, preflight.status, 1, "код")
	equal(t, preflight.answer["ok"], any(false), "ok")
	sameList(t, gapIDs(preflight), []string{"skills"}, "пробелы")
	equal(t, preflight.list("gaps")[0].(obj)["message"], any("скиллы не найдены в skillPaths ("+root+"/skills): missing-skill"), "текст пробела")

	refused := run(t, root, worktree, nil)
	equal(t, refused.status, 1, "код")
	match(t, refused.stderr, `прогон не начат — окружение не готово:\n.*- скиллы не найдены`, "лог")
	equal(t, len(refused.launches), 0, "запусков")
	if _, err := os.Stat(filepath.Join(root, ".llamer")); err == nil {
		t.Error("до проверки окружения артефактов нет")
	}
}

func TestPreflightChecksDriversOfAllStages(t *testing.T) {
	t.Parallel()
	drivers := obj{"codex": obj{"bin": "codex-нет-такого"}}
	gapsOf := func(cfg obj) *result {
		return run(t, makeRepo(t, cfg), append(slices.Clone(worktree), "--preflight"), map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": ""})
	}

	r := gapsOf(with(config(), "drivers", drivers))
	sameList(t, gapIDs(r), []string{"auth-claude"}, "codex нет ни у одного этапа — не проверяется")
	match(t, r.stdout, `драйверу claude нечем авторизоваться: нет \S+/\.claude/\.credentials\.json и не задана ни одна из CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN — claude /login`, "текст")
	// Этап тестов в этом прогоне не поднимется (файлов под маски нет), но его драйвер проверяется до прогона.
	r = gapsOf(with(config(), "drivers", drivers, "stages", stages(config()["stages"].(obj), map[string][]string{"tests": {"claude", "codex"}})))
	sameList(t, gapIDs(r), []string{"auth-claude", "driver-codex"}, "пробелы")
	match(t, r.stdout, `драйвер codex: codex-нет-такого не запускается \(exec: "codex-нет-такого": executable file not found`, "текст")
}

func TestVersionAndDriverOnlyConfig(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, obj{"version": 2, "driver": "claude"})

	preflight := run(t, root, append(slices.Clone(worktree), "--preflight"), nil)
	ok(t, preflight)
	equal(t, preflight.stdout, "version: 3\nok: true\ngaps: []\n", "preflight")

	r := run(t, root, worktree, nil)
	ok(t, r)
	sameList(t, r.stages(), []string{"assess", "comments", "dedupe", "logic", "quality", "skeptic"}, "поднятые этапы")
	prompts := r.prompts()
	match(t, prompts["assess"], `- stage:assessing-review-scope`, "assess")
	match(t, prompts["logic"], `- stage:reviewing-business-logic`, "logic")
	match(t, prompts["quality"], `- stage:reviewing-code-quality`, "quality")
	match(t, prompts["skeptic"], `- stage:refuting-review-findings`, "skeptic")
	match(t, prompts["skeptic"], `written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer and the test reviewer\.`, "skeptic")
	entries, _ := os.ReadDir(filepath.Join(r.str("artifacts"), "stage-plugin", "skills"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sameList(t, names, []string{"assessing-review-scope", "refuting-review-findings", "reviewing-business-logic", "reviewing-code-quality"}, "скиллы прогона")
}

func TestHarnessHelpOnlyOnRequest(t *testing.T) {
	t.Parallel()
	r := run(t, makeRepo(t, config()), worktree, nil)

	ok(t, r)
	for _, l := range r.launches {
		stage, prompt, help := l["stage"].(string), l["prompt"].(string), l["help"].(string)
		match(t, prompt, "## Your Commands\n\nThe recording command is `review`; its commands and their formats are in `review --help`\\.\n\n## ", stage)
		noMatch(t, prompt, `Usage: review|\$ review`, stage)
		for _, name := range strs(l["commands"]) {
			match(t, help, `\n  `+name+`  [^\n]+\n`, stage+": "+name+" в перечне")
			match(t, help, `\n\$ review `+name+` --help\nUsage: review `+name+` \[options\][^\n]*\n[\s\S]*?\nOptions:\n`, stage+": справка "+name)
		}
	}
}

func TestStageTemplates(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	write(t, root, "docs/guide.md", "Руководство.\n")
	write(t, root, "tests/OrderTest.php", "<?php\n\n// Проверяет сумму заказа.\nfunction testTotal() {}\n")

	r := run(t, root, worktree, nil)

	ok(t, r)
	prompts := r.prompts()
	match(t, prompts["logic"], `^You are a Senior Software Engineer with expertise in domain logic`, "logic")
	match(t, prompts["quality"], `^You are a Senior Software Engineer with expertise in readability`, "quality")
	match(t, prompts["comments"], `^You are a Senior Software Engineer with expertise in code documentation`, "comments")
	match(t, prompts["comments"], "<added_comments>\ntests/OrderTest\\.php\nL3\\+\t// Проверяет сумму заказа\\.\nL4\tfunction testTotal\\(\\) \\{\\}", "comments")
	match(t, prompts["tests"], `^You are a Senior Test Engineer`, "tests")
	match(t, prompts["tests"], `Files:\n\n- tests/OrderTest\.php\n\n`, "тестам — только файлы под их маски")
	match(t, prompts["docs"], `^Документация\.\n\nFindings outside this task are not yours`, "docs")
	// Сводку пишут бизнес-логика и свой этап; остальные разбирающие — без неё.
	for _, l := range r.launches {
		stage := l["stage"].(string)
		has := strings.Contains(l["help"].(string), "$ review summary --help")
		switch stage {
		case "logic", "docs":
			equal(t, has, true, stage+": summary")
		case "quality", "comments", "tests":
			equal(t, has, false, stage+": summary")
		}
	}
	match(t, prompts["skeptic"], `written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer, the test reviewer and the docs stage\.`, "скептик")
}

// Комментарий для этапа comments — по синтаксису языка файла: заголовок
// Markdown и атрибут PHP — не комментарии.
func TestCommentsBySyntax(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	write(t, root, "scripts/ci/Readme.md", "# scripts/ci\n<!-- note -->\n")
	write(t, root, "scripts/ci/run.sh", "# run\nmake\n")
	write(t, root, "src/A.php", "#[Route]\n# note\n// note\nreturn 1;\n")
	write(t, root, "db/m.sql", "-- note\nSELECT 1;\n")

	r := run(t, root, worktree, nil)

	ok(t, r)
	block := regexp.MustCompile(`<added_comments>\n([\s\S]*?)\n</added_comments>`).FindStringSubmatch(r.prompts()["comments"])
	if block == nil {
		t.Fatal("в задании comments нет комментариев")
	}
	var files []string
	for _, line := range strings.Split(block[1], "\n") {
		if line != "" && !strings.HasPrefix(line, "L") && line != "…" {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	sameList(t, files, []string{"db/m.sql", "scripts/ci/run.sh", "src/A.php"}, "файлы с комментариями")
	match(t, block[1], "src/A\\.php\nL2\\+\t# note\nL3\\+\t// note\n", "комментарии PHP")
	noMatch(t, block[1], `L1\+\t#\[Route\]`, "атрибут PHP — не комментарий")
}

// Пустая строка в конце ханка — строка контекста « »: её потеря ломает разбор диффа.
func TestHunkEndingWithBlankContext(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	// Последний файл диффа: обрезку по краям вывода git видит только он.
	write(t, root, "zz.txt", "x\n\n")
	git(t, root, "add", "zz.txt")
	git(t, root, "commit", "-q", "-m", "notes")
	write(t, root, "zz.txt", "y\n\n")

	ok(t, run(t, root, []string{"--base", "HEAD", "--head", "worktree"}, nil))
}

func TestConfigPrompt(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg["prompt"] = obj{"protocol": "`nav` is up.", "plan": "Plan with `nav def`."}
	cfg["stages"].(obj)["quality"] = obj{"drivers": []string{"claude"}, "prompt": obj{"plan": "Quality plan."}}
	cfg["stages"].(obj)["docs"].(obj)["prompt"] = obj{"protocol": "docs nav."}
	root := makeRepo(t, cfg)
	write(t, root, "docs/guide.md", "Руководство.\n")
	prompts := run(t, root, worktree, nil).prompts()
	plain := run(t, makeRepo(t, config()), worktree, nil).prompts()

	for _, stage := range []string{"assess", "logic", "skeptic"} {
		match(t, prompts[stage], "such files open by direct path\\.\n`nav` is up\\.\nLinters, tests", stage)
	}
	match(t, prompts["docs"], "such files open by direct path\\.\ndocs nav\\.\nLinters, tests", "протокол этапа перекрывает общий")
	match(t, prompts["docs"], "## Your Plan\n\nPlan with `nav def`\\.", "план этапа без своего — общий")
	match(t, prompts["logic"], "## Your Plan\n\nPlan with `nav def`\\.\n\nThen work through the plan\\.", "logic")
	match(t, prompts["quality"], `## Your Plan\n\nQuality plan\.\n\nThen work through the plan\.`, "quality")
	noMatch(t, prompts["logic"], `or a text search`, "logic")
	match(t, plain["logic"], `direct path\.\nLinters, tests`, "без prompt")
	match(t, plain["logic"], `## Your Plan\n\nPresent a plan before your first search: [^\n]*, or a text search\.`, "без prompt — поиск")
}

func TestStageOnTwoDrivers(t *testing.T) {
	t.Parallel()
	// logic — на уровне оценщика (2), quality — на 2, comments — на 1; оценщик и дедупликация — codex, скептик — claude.
	cfg := with(config(),
		"drivers", obj{"claude": obj{"levels": levels("claude")}, "codex": obj{"levels": levels("codex")}},
		"stages", stages(config()["stages"].(obj), map[string][]string{
			"assess": {"codex"}, "comments": {"codex"}, "dedupe": {"codex"}, "logic": {"claude", "codex"}, "quality": {"codex", "claude"}, "skeptic": {"claude"},
		}))

	r := run(t, makeRepo(t, cfg), worktree, nil)

	ok(t, r)
	launched := map[string]string{}
	for _, l := range r.launches {
		argv := argvOf(l)
		model := argAfter(argv, "-m")
		if l["driver"] == "claude" {
			model = argAfter(argv, "--model") + "/" + argAfter(argv, "--effort")
		}
		launched[l["stage"].(string)] = l["driver"].(string) + " " + model
	}
	equal(t, fmt.Sprint(launched), fmt.Sprint(map[string]string{
		"assess": "codex codex-1", "comments": "codex codex-1", "dedupe": "codex codex-1",
		"logic·claude": "claude claude-2/effort-2", "logic·codex": "codex codex-2",
		"quality·claude": "claude claude-2/effort-2", "quality·codex": "codex codex-2", "skeptic": "claude claude-2/effort-2",
	}), "экземпляры и их уровни")
	match(t, r.stderr, `этап «logic·codex» пошёл: codex, уровень 2 \(codex-2/effort-2\)`, "лог")

	briefOf := func(name string) obj {
		var b obj
		if err := json.Unmarshal([]byte(read(t, filepath.Join(r.str("artifacts"), "briefs", name+".json"))), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	equal(t, fmt.Sprint(briefOf("quality·codex")["siblings"]), "[quality·claude]", "экземпляр скрывает находки соседа по этапу")
	equal(t, fmt.Sprint(briefOf("skeptic")["siblings"]), "[]", "у скептика соседей нет")
	var written []string
	journals := filepath.Join(r.str("artifacts"), "journal")
	entries, _ := os.ReadDir(journals)
	for _, e := range entries {
		for _, line := range strings.Split(strings.TrimSpace(read(t, filepath.Join(journals, e.Name()))), "\n") {
			var event obj
			_ = json.Unmarshal([]byte(line), &event)
			if event["type"] == "finding" {
				written = append(written, fmt.Sprint(event["stage"], " ", event["driver"]))
			}
		}
	}
	slices.Sort(written)
	sameList(t, written, []string{"comments codex", "logic·claude claude", "logic·codex codex", "quality·claude claude", "quality·codex codex"}, "находки помечены драйвером")
	findings := r.list("findings")
	equal(t, len(findings) == 1 && findings[0].(obj)["driver"] != nil, true, "находка в отчёте — с драйвером")
	var summaries []string
	for _, s := range r.list("summaries") {
		summaries = append(summaries, s.(obj)["stage"].(string))
	}
	slices.Sort(summaries)
	sameList(t, summaries, []string{"logic·claude", "logic·codex"}, "сводки экземпляров")
	// Лимиты подписки — по драйверу: claude из rate_limit_event потока, codex из журнала сессии.
	equal(t, fmt.Sprint(r.answer["limits"]), "map[claude:map[reason:<nil> status:allowed windows:map[five_hour:map[resetsAt:1794480600 usedPercent:45] seven_day:map[resetsAt:1794787200 usedPercent:58]]] codex:map[reason:<nil> status:allowed windows:map[seven_day:map[resetsAt:1794213888 usedPercent:70]]]]", "лимиты")
	match(t, r.stderr, "лимиты codex: неделя — использовано 70%, осталось 30%, сброс 2026-11-09 08:44 UTC\n", "лог лимитов codex")
	match(t, r.stderr, `лимиты claude: 5 часов — использовано 45%, осталось 55%, сброс 2026-11-12 10:50 UTC; неделя — `, "лог лимитов claude")

	prompts := r.prompts()
	listed := "the business-logic reviewer (logic·claude), the business-logic reviewer (logic·codex), the code-quality reviewer (quality·codex), the code-quality reviewer (quality·claude), the comments reviewer, the test reviewer and the docs stage"
	equal(t, strings.Contains(prompts["skeptic"], "written by different stages: "+listed+"."), true, "скептик называет экземпляры")
	equal(t, strings.Contains(prompts["dedupe"], "findings from "+listed+"."), true, "дедупликация называет экземпляры")
	for _, name := range []string{"logic·claude", "logic·codex"} {
		match(t, prompts[name], `^You are a Senior Software Engineer with expertise in domain logic`, name+": задание этапа общее")
	}
}

func TestFailedInstanceOfStageDoneElsewhere(t *testing.T) {
	t.Parallel()
	cfg := with(config(),
		"drivers", obj{"claude": obj{"levels": levels("claude")}, "codex": obj{"levels": levels("codex"), "env": obj{"STUB_FAIL_STAGE": "logic·codex"}}},
		"stages", stages(config()["stages"].(obj), map[string][]string{"logic": {"claude", "codex"}}))

	r := run(t, makeRepo(t, cfg), worktree, nil)

	ok(t, r)
	equal(t, len(r.list("gaps")), 0, "недостачи нет")
	notices := strs(r.answer["notices"])
	equal(t, len(notices), 1, "сообщений")
	match(t, notices[0], `^этап «logic·codex» сорвался: .+ — этап «logic» отработал на другом драйвере$`, "сообщение")
	match(t, r.stderr, `этап «logic·codex» сорвался: .+ — этап «logic» отработал на другом драйвере`, "лог")
}

func TestBrokenConfigStopsRun(t *testing.T) {
	t.Parallel()
	for _, cfg := range []obj{with(config(), "stagez", []any{}), with(config(), "version", 1)} {
		r := run(t, makeRepo(t, cfg), worktree, nil)
		equal(t, r.status, 1, "код")
		match(t, r.stderr, `\.llamer\.json: (неизвестные ключи: stagez|version: поддерживается 2)`, "лог")
		equal(t, len(r.launches), 0, "запусков")
	}
}

func TestNothingToReviewAndScopeFailure(t *testing.T) {
	t.Parallel()
	empty := makeRepo(t, config())
	git(t, empty, "checkout", "-q", "-f", "main")
	git(t, empty, "clean", "-qfd", "--", "src")
	git(t, empty, "checkout", "-q", "-b", "feature/empty")

	nothing := run(t, empty, worktree, nil)
	ok(t, nothing)
	sameList(t, keysOf(t, nothing.stdout), resultKeys, "поля результата")
	equal(t, fmt.Sprint(nothing.answer["findings"], nothing.answer["stages"], nothing.answer["limits"], nothing.answer["cost"].(obj)["usd"]), "[] [] map[] 0", "пустой результат")
	match(t, read(t, nothing.str("report")), `Разбирать нечего`, "report.md")
	equal(t, len(nothing.launches), 0, "запусков")

	// Вторая исключённая ревизия — без общей истории с головой: открыть вход это
	// не мешает, вычислить изменения — да.
	lonely := makeRepo(t, config())
	git(t, lonely, "checkout", "-q", "--orphan", "lonely")
	git(t, lonely, "commit", "-q", "--allow-empty", "-m", "сирота")
	git(t, lonely, "checkout", "-q", "feature/dev-7-orders")
	broken := run(t, lonely, append(slices.Clone(worktree), "--exclude", "lonely"), nil)
	equal(t, broken.status, 1, "код")
	equal(t, broken.answer["ok"], any(false), "ok")
	failure := broken.answer["failure"].(obj)
	equal(t, failure["reason"], any("scope"), "причина")
	match(t, failure["detail"].(string), `и lonely нет общей истории`, "подробность")
	equal(t, len(broken.launches), 0, "запусков")
}

func TestAssessorFailure(t *testing.T) {
	t.Parallel()
	r := run(t, makeRepo(t, with(config(), "drivers", stubEnvOf("STUB_FAIL_STAGE", "assess"))), worktree, nil)

	equal(t, r.status, 1, "код")
	equal(t, r.answer["ok"], any(false), "ok")
	equal(t, fmt.Sprint(r.answer["limits"].(obj)["claude"].(obj)["windows"].(obj)["seven_day"]), "map[resetsAt:1794787200 usedPercent:58]", "лимиты в результате")
	match(t, r.stderr, `лимиты claude: 5 часов — использовано 45%`, "лимиты в логе")
}

func TestSigtermMidStage(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	root := makeRepo(t, with(config(), "drivers", stubEnvOf("STUB_HANG_STAGE", "assess")))
	git(t, root, "checkout", "-q", "-f", "main")
	done := make(chan *result)
	// Голова — не HEAD: llamer выкладывает её отдельным worktree.
	go func() {
		done <- run(t, root, []string{"--exclude", "main", "--head", "feature/dev-7-orders"}, map[string]string{"TMPDIR": tmp})
	}()
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
	temporaries := func() []string {
		entries, _ := os.ReadDir(tmp)
		var names []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "llamer-") {
				names = append(names, regexp.MustCompile(`[^-]+$`).ReplaceAllString(e.Name(), ""))
			}
		}
		slices.Sort(names)
		return names
	}
	sameList(t, temporaries(), []string{"llamer-run-", "llamer-tree-"}, "временное прогона и голова выложены")

	if err := syscall.Kill(pids.Llamer, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	r := <-done

	equal(t, r.status, 143, "код\n"+r.stderr)
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	// Убитые процессы ещё мгновение висят зомби, пока их не приберёт init.
	for wait := 0; wait < 50 && (alive(pids.Stage) || alive(pids.Command)); wait++ {
		time.Sleep(20 * time.Millisecond)
	}
	equal(t, fmt.Sprint(alive(pids.Stage), alive(pids.Command)), "false false", "этап и его команда остановлены")
	sameList(t, temporaries(), nil, "временное убрано")
	equal(t, len(strings.Split(git(t, root, "worktree", "list"), "\n")), 1, "worktree головы снят с учёта git")
	runs, _ := filepath.Glob(filepath.Join(root, ".llamer", "2*"))
	for _, name := range []string{"оценщик.prompt.md", "оценщик.jsonl"} {
		if _, err := os.Stat(filepath.Join(runs[0], name)); err != nil {
			t.Errorf("записанные артефакты на месте: %v", err)
		}
	}
}
