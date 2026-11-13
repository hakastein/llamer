package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	project = "group/demo"
	iid     = "7"
)

// changesOf — /changes GitLab: дифф файла без заголовков, с первого ханка.
func changesOf(t *testing.T, root, base, head string) []obj {
	var out []obj
	for _, path := range strings.Split(git(t, root, "diff", "--name-only", base, head), "\n") {
		diff := git(t, root, "diff", base, head, "--", path)
		out = append(out, obj{"new_path": path, "old_path": path, "new_file": regexp.MustCompile(`(?m)^new file mode`).MatchString(diff),
			"deleted_file": false, "renamed_file": false, "diff": diff[strings.Index(diff, "@@"):] + "\n"})
	}
	return out
}

type mrRun struct {
	root, base, head, mark, mr string
	gitlab                     *gitlab
}

// pipeline — реквест из настоящего репозитория: дерево стоит на голове ветки,
// как клон CI, целевая ветка — в origin, куда её фетчит вызывающий. Наш
// открытый тред прошлого прогона висит на src/Order.php — его снимет этап
// тредов. markOf даёт ревизию прошлого прогона: она встаёт меткой в заметку-статус.
func pipeline(t *testing.T, cfg obj, markOf func(root string) string) *mrRun {
	root := makeRepo(t, cfg)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "finish")
	p := &mrRun{root: root, base: git(t, root, "merge-base", "main", "HEAD"), head: git(t, root, "rev-parse", "HEAD")}
	git(t, root, "update-ref", "refs/remotes/origin/main", "main")
	discussions := []any{obj{"id": "old-thread", "notes": []obj{{
		"body":     "Верни 3.\n\n<sub>ревизия</sub><!-- llamer-rev:" + p.base + " -->",
		"author":   obj{"username": "bot"},
		"position": obj{"position_type": "text", "new_path": "src/Order.php", "new_line": 4},
	}}}}
	if markOf != nil {
		p.mark = markOf(root)
		discussions = append(discussions, obj{"id": "last-status", "individual_note": true, "notes": []obj{{
			"body": "<!-- llamer-status -->\nСтатус.\n\n<sub>ревизия</sub><!-- llamer-rev:" + p.mark + " -->", "author": obj{"username": "bot"},
		}}})
	}
	p.gitlab = startGitlab(t, project, iid,
		obj{"title": "Заказы", "source_branch": "feature/dev-7-orders", "target_branch": "main",
			"diff_refs": obj{"base_sha": p.base, "head_sha": p.head, "start_sha": p.base}},
		changesOf(t, root, p.base, p.head),
		[]obj{{"head_commit_sha": p.head, "base_commit_sha": p.base, "start_commit_sha": p.base}},
		discussions)
	p.mr = p.gitlab.URL + "/" + project + "/-/merge_requests/" + iid
	return p
}

func onlyReads(t *testing.T, g *gitlab, why string) {
	t.Helper()
	if writes := g.writes(); len(writes) > 0 {
		t.Errorf("%s: в GitLab записано %v", why, writes)
	}
}

func TestMergeRequestReport(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), nil)

	r := run(t, p.root, []string{"--mr", p.mr}, nil)

	ok(t, r)
	equal(t, r.str("input"), "gitlab", "вход")
	equal(t, len(r.list("findings")), 1, "находок")
	resolved := r.list("resolvedThreads")
	equal(t, len(resolved), 1, "снятых тредов")
	equal(t, resolved[0].(obj)["id"].(string)+" "+resolved[0].(obj)["place"].(string), "old-thread src/Order.php:4", "снятый тред")
	match(t, read(t, r.str("report")), `## Снятые треды\n\n- src/Order\.php:4 — `, "report.md")
	equal(t, slices.Contains(r.stages(), "threads"), true, "этап тредов поднят")
	onlyReads(t, p.gitlab, "без публикации")
	for _, req := range p.gitlab.all() {
		equal(t, req.Token, token, "токен запроса")
	}
}

func TestMergeRequestPublish(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), nil)

	r := run(t, p.root, []string{"--mr", p.mr, "--publish", "gitlab"}, nil)

	ok(t, r)
	equal(t, r.stdout, "", "при публикации stdout пуст")
	writes := p.gitlab.writes()
	var notes []string
	var resolve, thread *request
	for i, w := range writes {
		switch {
		case strings.HasSuffix(w.Path, "/notes"):
			notes = append(notes, w.Body["body"].(string))
		case w.Method == "PUT":
			resolve = &writes[i]
		case w.Method == "POST" && strings.HasSuffix(w.Path, "/discussions"):
			if thread != nil {
				t.Error("повтор снят дедупликацией и до MR не доехал")
			}
			thread = &writes[i]
		}
	}
	match(t, notes[0], `<!-- llamer-assessment -->`, "заметка о рисках")
	match(t, notes[0], `уровне 2 из 5`, "заметка о рисках")
	equal(t, slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "**logic**") }), true, "сводка разбирающих этапов")
	last := notes[len(notes)-1]
	match(t, last, `<!-- llamer-status -->`, "статус")
	match(t, last, `llamer-rev:`+p.head, "метка статуса")

	match(t, resolve.Path, `/discussions/old-thread$`, "закрытый тред")
	equal(t, resolve.Body["resolved"], any(true), "закрытие")

	position := thread.Body["position"].(obj)
	equal(t, position["new_path"], any("src/Fresh.php"), "тред")
	equal(t, position["head_sha"], any(p.head), "тред")
	equal(t, position["line_range"].(obj)["start"].(obj)["type"], any("new"), "тред")
	body := thread.Body["body"].(string)
	match(t, body, `llamer-rev:`, "метка треда")
	// Заглушка драйвера напечатала токен GitLab в тексте находки.
	match(t, body, `токен \*\*\*REDACTED\*\*\*`, "токен в треде вымаран")
	for _, w := range writes {
		if body, _ := json.Marshal(w.Body); strings.Contains(string(body), token) {
			t.Errorf("токен ушёл в GitLab: %s", body)
		}
	}
	// И в причине закрытия треда, которая идёт в лог.
	match(t, r.stderr, `закрыт тред src/Order\.php:4 — .* токен \*\*\*REDACTED\*\*\*`, "лог")
	equal(t, strings.Contains(r.stderr, token), false, "токен в stderr")
}

func TestDryRun(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), nil)

	r := run(t, p.root, []string{"--mr", p.mr, "--publish", "gitlab", "--dry-run"}, nil)

	ok(t, r)
	equal(t, len(r.list("findings")), 1, "находок")
	onlyReads(t, p.gitlab, "--dry-run")
}

func TestMergeRequestWithoutToken(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), nil)

	r := run(t, p.root, []string{"--mr", p.mr, "--preflight"}, map[string]string{"GITLAB_TOKEN": ""})

	equal(t, r.status, 1, "код")
	sameList(t, gapIDs(r), []string{"gitlab-token"}, "пробелы")
	equal(t, len(p.gitlab.all()), 0, "запросов к GitLab")

	short := run(t, p.root, []string{"--mr", "group/demo!7", "--preflight"}, nil)
	sameList(t, gapIDs(short), []string{"gitlab-url"}, "project!iid без адреса")
	match(t, short.stdout, `адрес GitLab неизвестен: дай ссылку на MR целиком или задай GITLAB_URL или CI_SERVER_URL`, "текст")
	bad := run(t, p.root, []string{"--mr", "что-то", "--preflight"}, nil)
	sameList(t, gapIDs(bad), []string{"mr-ref"}, "ссылка не разбирается")
}

func TestSinceLastMarkInHistory(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), func(root string) string { return git(t, root, "rev-parse", "HEAD~1") })

	r := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)

	ok(t, r)
	equal(t, r.str("base"), p.mark, "изменения задачи считаются от метки")
	match(t, r.stderr, `только изменения после прошлого прогона `+p.mark[:8], "лог")
}

func TestSinceLastMarkOutsideHistory(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), func(root string) string { return git(t, root, "rev-parse", "main") })

	r := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)

	ok(t, r)
	equal(t, r.str("base"), p.base, "весь реквест")
	match(t, r.stderr, `вне истории головы пропущены.*`+p.mark[:8], "лог")
}

func TestSinceLastWithoutStatusMark(t *testing.T) {
	t.Parallel()
	// Метка на нашем треде — не метка прогона: в счёт идёт только заметка-статус.
	p := pipeline(t, config(), nil)

	r := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)

	ok(t, r)
	equal(t, r.str("base"), p.base, "весь реквест")
	match(t, r.stderr, `годной метки прошлого прогона нет`, "лог")
}

func TestSinceLastNothingNew(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), func(root string) string { return git(t, root, "rev-parse", "HEAD") })

	r := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)

	ok(t, r)
	equal(t, r.str("base"), p.head, "база — голова")
	equal(t, len(r.list("findings")), 0, "находок")
	equal(t, len(r.launches), 0, "запусков")

	// С публикацией — только строка в логе: статус прошлого прогона остаётся в силе.
	published := run(t, p.root, []string{"--mr", p.mr, "--since-last", "--publish", "gitlab"}, nil)
	ok(t, published)
	match(t, published.stderr, `новых своих изменений нет — в MR ничего не публикуется`, "лог")
	onlyReads(t, p.gitlab, "в MR ничего не записано")
}

func TestNothingOwnPublishesNote(t *testing.T) {
	t.Parallel()
	p := pipeline(t, config(), nil)
	// Голова MR совпадает с целевой веткой: своих изменений нет.
	git(t, p.root, "update-ref", "refs/remotes/origin/main", p.head)

	r := run(t, p.root, []string{"--mr", p.mr, "--publish", "gitlab"}, nil)

	ok(t, r)
	writes := p.gitlab.writes()
	equal(t, len(writes), 1, "одна заметка")
	match(t, writes[0].Body["body"].(string), `^<!-- llamer-status -->\n_Разбирать нечего: голова не добавляет изменений к базе разбора\. Ревью пропущено\._\n\n<details><summary>Что ревьюилось: 0 файлов</summary>`, "заметка о пропуске")
}

func statusNote(g *gitlab) string {
	for _, w := range g.writes() {
		if body, ok := w.Body["body"].(string); ok && w.Method == "POST" && strings.HasPrefix(body, "<!-- llamer-status -->") {
			return body
		}
	}
	return ""
}

func TestSinceLastAfterFailedStage(t *testing.T) {
	t.Parallel()
	p := pipeline(t, with(config(), "drivers", stubEnvOf("STUB_FAIL_STAGE", "logic")), nil)

	failed := run(t, p.root, []string{"--mr", p.mr, "--since-last", "--publish", "gitlab"}, nil)
	equal(t, failed.status, 1, "сорванный этап — неуспешный прогон")
	match(t, failed.stderr, `этап «logic» сорвался`, "лог")
	status := statusNote(p.gitlab)
	equal(t, status != "", true, "статус опубликован")
	noMatch(t, status, `llamer-rev`, "статус без метки")

	next := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)
	equal(t, next.str("base"), p.base, "неразобранное после срыва не пропущено")
	match(t, next.stderr, `годной метки прошлого прогона нет`, "лог")
}

func TestSinceLastAfterStoppedStage(t *testing.T) {
	t.Parallel()
	p := pipeline(t, with(config(), "drivers", stubEnvOf("STUB_STOP_STAGE", "logic")), nil)

	stopped := run(t, p.root, []string{"--mr", p.mr, "--since-last", "--publish", "gitlab"}, nil)
	ok(t, stopped)
	status := statusNote(p.gitlab)
	equal(t, status != "", true, "статус опубликован")
	noMatch(t, status, `llamer-rev`, "статус без метки")

	next := run(t, p.root, []string{"--mr", p.mr, "--since-last"}, nil)
	equal(t, next.str("base"), p.base, "неразобранное после stop не пропущено")
	match(t, next.stderr, `годной метки прошлого прогона нет`, "лог")
}
