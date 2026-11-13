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
	"testing"
)

// minimal — конфиг из версии, драйвера и встроенных этапов на claude, extra — поверх.
func minimal(extra obj) obj {
	out := obj{"version": 2, "driver": "claude"}
	for k, v := range extra {
		out[k] = v
	}
	own, _ := extra["stages"].(obj)
	out["stages"] = stages(own, nil)
	return out
}

// preflightOf — пробелы окружения при конфиге cfg, переданном через --config.
func preflightOf(t *testing.T, root string, cfg any, env map[string]string) *result {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "review.json", string(data))
	return run(t, root, append(slices.Clone(worktree), "--config", filepath.Join(dir, "review.json"), "--preflight"), env)
}

func configGap(t *testing.T, r *result) string {
	t.Helper()
	for _, g := range r.list("gaps") {
		if g.(obj)["id"] == "config" {
			return g.(obj)["message"].(string)
		}
	}
	t.Fatalf("пробела config нет:\n%s", r.stdout)
	return ""
}

func TestConfigErrors(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	stagesWith := func(byKey map[string][]string) obj { return obj{"stages": stages(nil, byKey)} }
	cases := []struct {
		raw     obj
		message string
	}{
		{with(minimal(nil), "version", 1), `version: поддерживается 2`},
		{minimal(obj{"extra": true}), `неизвестные ключи: extra`},
		{minimal(obj{"service": obj{}}), `неизвестные ключи: service`},
		{with(minimal(nil), "stages", []any{}), `stages: должен быть объектом`},
		{with(minimal(nil), "driver", nil), `driver: одно из claude, codex`},
		{minimal(obj{"stages": obj{"logic": obj{"task": "x"}}}), `stages\.logic: неизвестные ключи: task`},
		{minimal(obj{"stages": obj{"docs": obj{"mandate": "x", "paths": []string{"docs/**"}}}}), `stages\.docs: неизвестные ключи: mandate`},
		{minimal(obj{"stages": obj{"assess": obj{"paths": []string{"src/**"}}}}), `stages\.assess: неизвестные ключи: paths`},
		{minimal(obj{"stages": obj{"comments": obj{"task": "x"}}}), `stages\.comments: неизвестные ключи: task`},
		{minimal(obj{"prompt": obj{"system": "x"}}), `prompt: неизвестные ключи: system`},
		{minimal(obj{"stages": obj{"quality": obj{"prompt": obj{"plan": ""}}}}), `stages\.quality\.prompt\.plan: должен быть непустой строкой`},
		{minimal(obj{"stages": obj{"docs": obj{"paths": []string{"docs/**"}}}}), `stages\.docs: нет ключей: task`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "id": "docs"}}}), `stages\.docs: неизвестные ключи: id`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "driver": "codex"}}}), `stages\.docs: неизвестные ключи: driver`},
		{minimal(obj{"stages": obj{"Docs": obj{"task": "x"}}}), `stages\.Docs: имя этапа не подходит`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "skills": []string{"a"}}}}), `stages\.docs\.skills: должен быть .*объектом`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "skills": obj{"always": []string{"a"}, "onDemand": []string{"a"}}}}}), `stages\.docs\.skills: скилл в обоих списках, always и onDemand: a`},
		{minimal(obj{"stages": obj{"logic": obj{"skills": obj{"onDemand": []string{"reviewing-business-logic"}}}}}), `stages\.logic\.skills: скилл в обоих списках`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "skills": obj{"sometimes": []any{}}}}}), `stages\.docs\.skills: неизвестные ключи: sometimes`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "level": 9}}}), `stages\.docs\.level: одно из assessed, 1, 2, 3, 4, 5`},
		{minimal(obj{"stages": obj{"assess": obj{"level": "assessed"}}}), `stages\.assess\.level: одно из 1, 2, 3, 4, 5$`},
		{minimal(obj{"stages": obj{"threads": obj{"level": "assessed"}}}), `stages\.threads\.level: одно из 1, 2, 3, 4, 5$`},
		{minimal(obj{"drivers": obj{"gemini": obj{}}}), `drivers: неизвестные ключи: gemini`},
		{minimal(obj{"levels": levels("model")}), `неизвестные ключи: levels`},
		{minimal(obj{"drivers": obj{"claude": obj{"levels": levels("model")[1:]}}}), `drivers\.claude\.levels: в списке не меньше 5`},
		{minimal(obj{"drivers": obj{"codex": obj{"levels": append(levels("model")[1:], obj{"driver": "codex", "model": "m", "effort": "e"})}}}), `drivers\.codex\.levels\[4\]: неизвестные ключи: driver`},
		{minimal(obj{"drivers": obj{"codex": obj{"levels": append(levels("model")[1:], obj{"model": "m"})}}}), `drivers\.codex\.levels\[4\]: нет ключей: effort`},
		{minimal(obj{"stages": obj{"logic": obj{"drivers": "claude"}}}), `stages\.logic\.drivers: должен быть списком`},
		{minimal(obj{"stages": obj{"logic": obj{"drivers": []string{}}}}), `stages\.logic\.drivers: в списке не меньше 1`},
		{minimal(obj{"stages": obj{"logic": obj{"drivers": []string{"gemini"}}}}), `stages\.logic\.drivers\[0\]: одно из claude, codex`},
		{minimal(obj{"stages": obj{"docs": obj{"task": "x", "drivers": []string{"codex", "claude", "codex"}}}}), `stages\.docs\.drivers: элементы 0 и 2 повторяются`},
		{minimal(obj{"allow": []string{"rg; rm -rf /"}}), `allow\[0\]: «rg; rm -rf /» не подходит`},
		{minimal(obj{"stages": obj{"skeptic": obj{"allow": []string{"rg | sh"}}}}), `stages\.skeptic\.allow\[0\]: «rg \| sh» не подходит`},
		{minimal(obj{"skillPaths": []string{"${NOPE}/skills"}}), `переменная NOPE не задана`},
	}
	for _, key := range []string{"assess", "threads", "dedupe", "skeptic"} {
		cases = append(cases, struct {
			raw     obj
			message string
		}{minimal(stagesWith(map[string][]string{key: {"claude", "codex"}})), `stages\.` + key + `\.drivers: ` + key + ` — служебный этап, он идёт одним экземпляром: ровно один драйвер`})
	}
	for _, c := range cases {
		r := preflightOf(t, root, c.raw, nil)
		equal(t, r.status, 1, c.message)
		match(t, configGap(t, r), `^\S+/review\.json: .*`+c.message, c.message)
	}
	// Разбирающий этап на двух драйверах — не ошибка.
	ok(t, preflightOf(t, root, minimal(stagesWith(map[string][]string{"tests": {"codex", "claude"}})), nil))
}

func TestDriverOfAllStages(t *testing.T) {
	t.Parallel()
	cfg := obj{"version": 2, "driver": "codex", "skillPaths": []string{"skills"},
		"stages": obj{"logic": obj{"drivers": []string{"claude", "codex"}}, "docs": obj{"task": "Docs.", "paths": []string{"src/**"}}}}

	r := run(t, makeRepo(t, cfg), worktree, nil)

	ok(t, r)
	drivers := map[string]string{}
	for _, l := range r.launches {
		drivers[l["stage"].(string)] = l["driver"].(string)
	}
	equal(t, len(drivers), 8, "запусков")
	for stage, driver := range drivers {
		want := "codex"
		if stage == "logic·claude" {
			want = "claude"
		}
		equal(t, driver, want, stage)
	}
	equal(t, drivers["logic·codex"], "codex", "logic на обоих драйверах")
}

func TestBuiltinStageSettingsFromConfig(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg["drivers"] = obj{"claude": obj{"levels": levels("claude")}}
	logic := cfg["stages"].(obj)["logic"].(obj)
	logic["level"] = 4
	cfg["stages"].(obj)["aaa"] = obj{"task": "Своё.", "drivers": []string{"claude"}}
	// Лестница codex — своя: она не задевает модели этапов claude.
	cfg["drivers"].(obj)["codex"] = obj{"levels": levels("gpt")}

	r := run(t, makeRepo(t, cfg), worktree, nil)

	ok(t, r)
	argv := argvOf(r.launch(t, "logic"))
	equal(t, argAfter(argv, "--model")+"/"+argAfter(argv, "--effort"), "claude-4/effort-4", "level этапа из конфига")
	argv = argvOf(r.launch(t, "quality"))
	equal(t, argAfter(argv, "--model"), "claude-2", "встроенный уровень quality — 2")
	match(t, r.prompts()["skeptic"], `the test reviewer, the aaa stage and the docs stage\.`, "свои этапы — после встроенных")
}

func TestSkillPathsExpandEnvAndConfigDir(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	tools := t.TempDir()
	write(t, tools, "skills/from-env/SKILL.md", "---\nname: from-env\n---\n")
	dir := t.TempDir()
	write(t, dir, "local/near/SKILL.md", "---\nname: near\n---\n")
	cfg := minimal(obj{"skillPaths": []string{"${TOOLS}/skills", "local"}, "stages": obj{"logic": obj{"skills": obj{"always": []string{"from-env", "near"}}}}})
	data, _ := json.Marshal(cfg)
	write(t, dir, "review.json", string(data))

	r := run(t, root, append(slices.Clone(worktree), "--config", filepath.Join(dir, "review.json"), "--preflight"), map[string]string{"TOOLS": tools})

	ok(t, r)
	empty := t.TempDir()
	write(t, empty, "skills/.keep", "")
	r = run(t, root, append(slices.Clone(worktree), "--config", filepath.Join(dir, "review.json"), "--preflight"), map[string]string{"TOOLS": empty})
	equal(t, r.list("gaps")[0].(obj)["message"], any("скиллы не найдены в skillPaths ("+r.env["TOOLS"]+"/skills, "+dir+"/local): from-env"), "пробел называет каталоги")
}

func TestDriverEnvExpansion(t *testing.T) {
	t.Parallel()
	cfg := with(config(), "drivers", obj{"claude": obj{"env": obj{"CLAUDE_HOME_HINT": "${TOOLS}/claude", "PLAIN": "x", "A": "${NOPE}", "B": "${TOOLS}:${NOPE}"}}})

	r := run(t, makeRepo(t, cfg), worktree, map[string]string{"TOOLS": "/opt/tools"})

	ok(t, r)
	env := strs(r.launch(t, "assess")["env"])
	for _, name := range []string{"CLAUDE_HOME_HINT", "PLAIN"} {
		equal(t, slices.Contains(env, name), true, name+" передан")
	}
	equal(t, slices.Contains(env, "A"), false, "A с незаданной NOPE не передаётся")
	equal(t, slices.Contains(env, "B"), false, "B с одной незаданной из двух не передаётся")
	equal(t, slices.Contains(env, "TOOLS"), false, "переменная окружения прогона в этап не идёт")
}

func TestDuplicateSkillNames(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	dir := t.TempDir()
	write(t, dir, "a/same/SKILL.md", "a")
	write(t, dir, "b/same/SKILL.md", "b")
	write(t, dir, "b/other/SKILL.md", "b")

	r := preflightOf(t, root, minimal(obj{"skillPaths": []string{dir + "/a", dir + "/b"}}), nil)
	equal(t, r.list("gaps")[0].(obj)["message"], any("скилл same есть в двух каталогах: "+dir+"/a/same и "+dir+"/b/same"), "пробел")

	r = preflightOf(t, root, minimal(obj{"skillPaths": []string{dir + "/b"}, "stages": obj{"logic": obj{"skills": obj{"always": []string{"same", "other", "nope"}}}}}), nil)
	match(t, r.list("gaps")[0].(obj)["message"].(string), `\): nope$`, "не найден только nope")
}

func TestBuiltinSkillWinsOverSameNamed(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, with(config(), "skillPaths", []string{"skills", "own"}))
	write(t, root, "own/reviewing-code-quality/SKILL.md", "свой")
	write(t, root, ".git/info/exclude", "own/\n")

	r := run(t, root, worktree, nil)

	ok(t, r)
	text := read(t, filepath.Join(r.str("artifacts"), "stage-plugin", "skills", "reviewing-code-quality", "SKILL.md"))
	equal(t, text != "свой" && strings.HasPrefix(text, "---\nname: reviewing-code-quality"), true, "этап получает встроенный")
}

func TestLocalConfigMergePatch(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	file := filepath.Join(root, "ci", "review.json")
	base := minimal(obj{"skillPaths": []string{"../skills"}, "allow": []string{"rg", "git log"},
		"drivers": obj{"codex": obj{"bin": "codex-ci", "levels": levels("gpt")}},
		"stages":  obj{"logic": obj{"drivers": []string{"codex"}, "level": 4}}})
	data, _ := json.Marshal(base)
	write(t, root, "ci/review.json", string(data))
	write(t, root, "ci/.llamer.local.json", `{"allow": ["rg"], "drivers": {"codex": {"bin": "codex-mine"}}, "stages": {"logic": {"drivers": null}, "tests": {"drivers": ["codex"]}}}`)
	write(t, root, ".git/info/exclude", "ci/\n")

	preflight := run(t, root, append(slices.Clone(worktree), "--config", file, "--preflight"), nil)
	match(t, preflight.stdout, `драйвер codex: codex-mine не запускается`, "bin из local, соседние ключи объекта остались")

	write(t, root, "ci/.llamer.local.json", `{"allow": ["rg"], "drivers": {"codex": {"bin": "codex"}}, "stages": {"logic": {"drivers": null}}}`)
	r := run(t, root, append(slices.Clone(worktree), "--config", file), nil)
	ok(t, r)
	match(t, r.stderr, `конфиг: `+regexp.QuoteMeta(file+" + "+filepath.Join(root, "ci", ".llamer.local.json")), "источник конфига")
	logic := r.launch(t, "logic")
	equal(t, logic["driver"], any("claude"), "null снял переопределение — этап на корневом driver")
	equal(t, argAfter(argvOf(logic), "--model"), "claude-opus-5-5", "level 4 остался")
	equal(t, strings.Contains(argAfter(argvOf(logic), "--allowedTools"), "git log"), false, "массив заменён целиком")

	write(t, root, "ci/.llamer.local.json", `{"driver": "gemini"}`)
	broken := run(t, root, append(slices.Clone(worktree), "--config", file, "--preflight"), nil)
	match(t, configGap(t, broken), `review\.json \+ .*\.llamer\.local\.json: driver: одно из claude, codex`, "проверяется итог наложения")
}

// Маски `paths` своих этапов: этап поднимается, только если под маски попал файл изменений.
func TestStagePathMasks(t *testing.T) {
	t.Parallel()
	own := obj{}
	masks := map[string][]string{
		"deep":      {"src/**"},
		"php":       {"**/*.php"},
		"vue":       {"**/*.vue"},
		"fresh":     {"**/*Fresh.php"},
		"repo":      {"**/*Repository.php"},
		"all":       {"**"},
		"none":      {},
		"negated":   {"src/**", "!src/*.php"},
		"only-neg":  {"!docs/**"},
		"star":      {"src/*.php"},
		"star-deep": {"*.php"},
	}
	for name, paths := range masks {
		own[name] = obj{"task": "Этап " + name + ".", "paths": paths, "drivers": []string{"claude"}}
	}
	cfg := with(config(), "stages", stages(own, nil))

	r := run(t, makeRepo(t, cfg), worktree, nil)

	ok(t, r)
	var raised []string
	for _, stage := range r.stages() {
		if _, ok := masks[stage]; ok {
			raised = append(raised, stage)
		}
	}
	sameList(t, raised, []string{"all", "deep", "fresh", "none", "only-neg", "php", "star"}, "поднятые этапы")
	match(t, r.stderr, `этап «negated» не поднимается: в изменениях нет файлов под маски src/\*\*, !src/\*\.php`, "лог")
}

func TestClaudeLimitsFormats(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ event, limits, log string }{
		// Форма из документации SDK: одно окно на верхнем уровне, отказ с кодом.
		{`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1794787200,"utilization":1,"errorCode":"credits_required","rateLimitType":"seven_day"}}`,
			`map[reason:credits_required status:rejected windows:map[seven_day:map[resetsAt:1794787200 usedPercent:100]]]`,
			`лимиты claude: исчерпан \(credits_required\); неделя — использовано 100%, осталось 0%, сброс 2026-11-16 00:00 UTC`},
		{`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`,
			`map[reason:<nil> status:allowed_warning windows:map[]]`, `лимиты claude: на исходе\n`},
	} {
		r := run(t, makeRepo(t, with(config(), "drivers", obj{"claude": obj{"env": obj{"STUB_EVENTS": c.event}}})), worktree, nil)

		ok(t, r)
		equal(t, fmt.Sprint(r.answer["limits"].(obj)["claude"]), c.limits, "лимиты: последние сведения драйвера")
		match(t, r.stderr, c.log, "лог")
	}
}

func TestCodexLimitsFromSession(t *testing.T) {
	t.Parallel()
	week := `{"timestamp":"2026-11-06T10:57:40.928Z","ordinal":48,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":156537,"cached_input_tokens":115072,"cache_write_input_tokens":0,"output_tokens":1245,"reasoning_output_tokens":10,"total_tokens":157782},"last_token_usage":{"input_tokens":41598,"cached_input_tokens":40064,"cache_write_input_tokens":0,"output_tokens":449,"reasoning_output_tokens":0,"total_tokens":42047},"last_usage_includes_reasoning":true,"model_context_window":475000},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":70.0,"window_minutes":10080,"resets_at":1794213888},"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":null},"individual_limit":null,"spend_control_reached":null,"plan_type":"self_serve_business_prolite","rate_limit_reached_type":null}}}`
	depleted := `{"timestamp":"2026-11-12T09:37:19.666Z","ordinal":13,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"premium","limit_name":null,"primary":null,"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":null},"individual_limit":null,"spend_control_reached":null,"plan_type":null,"rate_limit_reached_type":"workspace_member_credits_depleted"}}}`
	done := `{"timestamp":"2026-11-12T09:37:19.671Z","ordinal":14,"type":"event_msg","payload":{"type":"task_complete","error":{"message":"Your workspace is out of credits.","codex_error_info":"usage_limit_exceeded"},"duration_ms":2880}}`
	// Два окна живьём не встречались: форма — по RateLimitSnapshot из исходников codex.
	two := `{"payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":12.5,"window_minutes":300,"resets_at":1794480600},"secondary":{"used_percent":70.0,"window_minutes":10080,"resets_at":1794213888},"rate_limit_reached_type":null}}}`
	hourly := `{"payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":70.0,"window_minutes":60,"resets_at":1794213888},"secondary":null,"rate_limit_reached_type":null}}}`
	for _, c := range []struct{ session, limits, log string }{
		{week + "\n" + `{"type":"token_usage_record","payload":{"usage":{}}}`, `map[reason:<nil> status:allowed windows:map[seven_day:map[resetsAt:1794213888 usedPercent:70]]]`, `лимиты codex: неделя — использовано 70%`},
		{week + "\n" + depleted + "\n" + done, `map[reason:workspace_member_credits_depleted status:rejected windows:map[]]`, `лимиты codex: исчерпан \(workspace_member_credits_depleted\)\n`},
		{done, ``, ``},
		{two, `map[reason:<nil> status:allowed windows:map[five_hour:map[resetsAt:1794480600 usedPercent:12.5] seven_day:map[resetsAt:1794213888 usedPercent:70]]]`, `лимиты codex: 5 часов — использовано 12.5%`},
		{hourly, `map[reason:<nil> status:allowed windows:map[60_minutes:map[resetsAt:1794213888 usedPercent:70]]]`, `лимиты codex: `},
	} {
		cfg := with(config(), "stages", stages(nil, map[string][]string{"all": {"codex"}}), "drivers", obj{"codex": obj{"env": obj{"STUB_SESSION": c.session}}})
		r := run(t, makeRepo(t, cfg), worktree, nil)

		ok(t, r)
		limits := r.answer["limits"].(obj)
		if c.limits == "" {
			equal(t, len(limits), 0, "нет token_count — нет и сведений")
			continue
		}
		equal(t, fmt.Sprint(limits["codex"]), c.limits, "лимиты codex")
		match(t, r.stderr, c.log, "лог")
	}
}

func TestCodexPricing(t *testing.T) {
	t.Parallel()
	session := `{"type":"token_usage_record","payload":{"usage":{"input_tokens":100000,"cached_input_tokens":0,"output_tokens":100000}}}` + "\n" +
		// 200K × $2 + 100K × $0.10 = $0.41 вдвое, 1K × $10 в полтора раза — промпт длиннее 272K.
		`{"type":"token_usage_record","payload":{"usage":{"input_tokens":300000,"cached_input_tokens":100000,"output_tokens":1000}}}`
	for _, c := range []struct {
		model   string
		cost    float64
		context float64
	}{
		{"gpt-6.1-terra", 0.6 + (200000*1+100000*0.05)*2/1e6 + 1000*5*1.5/1e6, 300000},
		{"gpt-6.1-sol", 1.2 + 0.835, 300000},
		{"gpt-неизвестная", -1, 300000},
	} {
		var ladder []obj
		for range 5 {
			ladder = append(ladder, obj{"model": c.model, "effort": "e"})
		}
		cfg := with(config(), "stages", stages(nil, map[string][]string{"all": {"codex"}}),
			"drivers", obj{"codex": obj{"levels": ladder, "env": obj{"STUB_SESSION": session}}})
		r := run(t, makeRepo(t, cfg), worktree, nil)

		ok(t, r)
		var cost struct {
			Stages []struct {
				Name          string
				ContextTokens float64
				Cost          *float64
			}
		}
		if err := json.Unmarshal([]byte(read(t, filepath.Join(r.str("artifacts"), "cost.json"))), &cost); err != nil {
			t.Fatal(err)
		}
		s := cost.Stages[0]
		equal(t, s.ContextTokens, c.context, c.model+": пик контекста — самый длинный запрос")
		if c.cost < 0 {
			equal(t, s.Cost == nil, true, c.model+": цены нет — null, а не $0")
			match(t, read(t, r.str("report")), `\$\? \+ \$\?|\$\?`, c.model+": $? в отчёте")
			equal(t, r.answer["stages"].([]any)[0].(obj)["usd"], nil, c.model+": usd")
			continue
		}
		equal(t, math.Abs(*s.Cost-c.cost) < 1e-9, true, c.model+": цена")
	}
}

func TestReadFilesThroughRegularFileParent(t *testing.T) {
	t.Parallel()
	root := makeRepo(t, config())
	write(t, root, "build/i18n-chunks.js", "")
	write(t, root, "build/i18n-routing.js", "")
	write(t, root, ".git/info/exclude", "build/\n")
	command, _ := json.Marshal(`rg 'build/i18n-chunks.js/i18n-routing.js' build missing.js build/i18n-chunks.js '` + root + `/build/i18n-routing.js'`)
	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":` + string(command) + `}}]}}`

	r := run(t, makeRepoWith(t, root, event), worktree, nil)

	ok(t, r)
	match(t, r.stderr, `\[оценщик\] прочитано файлов: 2`, "файлы в аргументах Bash — без пути сквозь обычный файл")
}

// makeRepoWith — тот же репозиторий с событием потока у заглушки claude.
func makeRepoWith(t *testing.T, root, event string) string {
	data, _ := json.MarshalIndent(with(config(), "drivers", obj{"claude": obj{"env": obj{"STUB_EVENTS": event}}}), "", "  ")
	if err := os.WriteFile(filepath.Join(root, ".llamer.json"), data, 0o666); err != nil {
		t.Fatal(err)
	}
	return root
}
