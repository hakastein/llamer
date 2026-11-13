package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// Тесты гоняют собранный бинарник llamer на заглушках claude и codex
// (testdata/stub) и, где нужен GitLab, на его подделке.

// token — секрет в окружении прогона: заглушки «вычитывают» его из дерева, наружу он уходит вымаранным.
const token = "glpat-...leak"

var llamer, stubs string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "llamer-test-")
	if err != nil {
		panic(err)
	}
	llamer = filepath.Join(dir, "llamer")
	stubs = filepath.Join(dir, "stubs")
	// LLAMER_BUILD_FLAGS — флаги сборки llamer, например -race: гонки ищутся в бинарнике, а не в тестах.
	flags := strings.Fields(os.Getenv("LLAMER_BUILD_FLAGS"))
	for _, build := range [][]string{append(flags, "-o", llamer, "."), {"-o", filepath.Join(stubs, "claude"), "./testdata/stub"}} {
		if out, err := exec.Command("go", append([]string{"build"}, build...)...).CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	if err := os.Symlink("claude", filepath.Join(stubs, "codex")); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("git %v: %v\n%s", args, err, exit.Stderr)
		}
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, root, path, text string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o666); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type obj = map[string]any

var builtin = []string{"assess", "threads", "logic", "quality", "comments", "tests", "dedupe", "skeptic"}

// stages — этапы конфига: все встроенные и own поверх них. Драйверы этапа —
// из drivers по ключу, иначе свои из own, иначе drivers["all"] (claude).
func stages(own obj, drivers map[string][]string) obj {
	all := []string{"claude"}
	if d, ok := drivers["all"]; ok {
		all = d
	}
	out := obj{}
	for _, key := range builtin {
		out[key] = obj{}
	}
	maps.Copy(out, own)
	for key, value := range out {
		stage := maps.Clone(value.(obj))
		switch {
		case drivers[key] != nil:
			stage["drivers"] = drivers[key]
		case stage["drivers"] == nil:
			stage["drivers"] = all
		}
		out[key] = stage
	}
	return out
}

// config — конфиг прогона по умолчанию: свой метод у logic и скептика, свой этап docs.
func config() obj {
	return obj{
		"version": 2, "driver": "claude", "skillPaths": []string{"skills"}, "allow": []string{"rg"},
		"stages": stages(obj{
			"logic":   obj{"skills": obj{"always": []string{"reviewing"}, "onDemand": []string{"backend"}}, "allow": []string{"git log"}},
			"skeptic": obj{"skills": obj{"always": []string{"refuting"}}},
			"docs":    obj{"task": "Документация.", "paths": []string{"docs/**"}},
		}, nil),
	}
}

// with — копия конфига с ключами верхнего уровня поверх.
func with(c obj, kv ...any) obj {
	out := maps.Clone(c)
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// levels — лестница из пяти уровней на моделях `<prefix>-<номер>`.
func levels(prefix string) []obj {
	var out []obj
	for n := 1; n <= 5; n++ {
		out = append(out, obj{"model": fmt.Sprintf("%s-%d", prefix, n), "effort": fmt.Sprintf("effort-%d", n)})
	}
	return out
}

func stubEnvOf(name, stage string) obj { return obj{"claude": obj{"env": obj{name: stage}}} }

// worktree — ветка с незакоммиченным против main, как разбирает свою ветку разработчик.
var worktree = []string{"--exclude", "main", "--head", "worktree"}

// makeRepo — ветка feature/dev-7-orders ответвилась от main, после чего main
// ушла вперёд: чужая правка из main в разбор попадать не должна. В рабочем
// дереве — незакоммиченная правка и неотслеживаемый файл.
func makeRepo(t *testing.T, cfg obj) string {
	t.Helper()
	// На macOS TMPDIR лежит за симлинком /var → /private/var, а llamer видит корень репозитория по git.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "config", "user.email", "dev@example.com")
	git(t, root, "config", "user.name", "dev")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, ".llamer.json", string(data))
	write(t, root, "skills/reviewing/SKILL.md", "---\nname: reviewing\n---\nМетод разбора.\n")
	write(t, root, "skills/refuting/SKILL.md", "---\nname: refuting\n---\nМетод скептика.\n")
	write(t, root, "skills/backend/SKILL.md", "---\nname: backend\ndescription: \"Use when the request touches PHP\"\n---\nСоглашения бэкенда.\n")
	write(t, root, "src/Order.php", "<?php\n\nfunction total() {\n    return 1;\n}\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "init")

	git(t, root, "checkout", "-q", "-b", "feature/dev-7-orders")
	write(t, root, "src/Order.php", "<?php\n\nfunction total() {\n    return 2;\n}\n")
	git(t, root, "commit", "-q", "-am", "feature")

	git(t, root, "checkout", "-q", "main")
	write(t, root, "src/Foreign.php", "<?php\n\nfunction foreign() {}\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "main moved on")
	git(t, root, "checkout", "-q", "feature/dev-7-orders")

	write(t, root, "src/Order.php", "<?php\n\nfunction total() {\n    return 3;\n}\n")
	write(t, root, "src/Fresh.php", "<?php\n\nfunction fresh() {\n    return 4;\n}\n")
	return root
}

type result struct {
	status         int
	stdout, stderr string
	launches       []obj
	answer         obj // stdout, разобранный как YAML
	home           string
	env            map[string]string
}

func (r *result) launch(t *testing.T, stage string) obj {
	t.Helper()
	for _, l := range r.launches {
		if l["stage"] == stage {
			return l
		}
	}
	t.Fatalf("этап %s не запускался", stage)
	return nil
}

func (r *result) stages() []string {
	var names []string
	for _, l := range r.launches {
		names = append(names, l["stage"].(string))
	}
	slices.Sort(names)
	return names
}

// prompts — задания этапов по имени.
func (r *result) prompts() map[string]string {
	out := map[string]string{}
	for _, l := range r.launches {
		out[l["stage"].(string)] = l["prompt"].(string)
	}
	return out
}

// started — секунда последнего прогона в каждом репозитории: каталог прогона
// назван с точностью до секунды, и прогоны подряд в одну секунду делили бы его.
var started sync.Map

// run — прогон llamer в root на заглушках: свой HOME, без личных кредов и конфигов.
func run(t *testing.T, root string, args []string, extra map[string]string) *result {
	t.Helper()
	if last, ok := started.Load(root); ok && !slices.Contains(args, "--preflight") {
		if wait := time.Until(last.(time.Time).Truncate(time.Second).Add(time.Second)); wait > 0 {
			time.Sleep(wait)
		}
	}
	started.Store(root, time.Now())
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	write(t, codexHome, "auth.json", `{"auth_mode":"stub"}`)
	env := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		env[name] = value
	}
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "GITLAB_URL", "CI_SERVER_URL", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY"} {
		delete(env, name)
	}
	maps.Copy(env, map[string]string{"HOME": home, "PATH": stubs + ":" + os.Getenv("PATH"), "CLAUDE_CODE_OAUTH_TOKEN": "stub-oauth-token-value",
		"CODEX_HOME": codexHome, "TMPDIR": home, "GITLAB_TOKEN": token})
	maps.Copy(env, extra)
	cmd := exec.Command(llamer, args...)
	cmd.Dir = root
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := &result{stdout: stdout.String(), stderr: stderr.String(), home: env["TMPDIR"], env: env}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.status = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(r.home, "launches.jsonl")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var l obj
			if err := json.Unmarshal([]byte(line), &l); err != nil {
				t.Fatal(err)
			}
			r.launches = append(r.launches, l)
		}
	}
	if strings.HasPrefix(r.stdout, "version:") {
		if err := yaml.Unmarshal([]byte(r.stdout), &r.answer); err != nil {
			t.Fatalf("stdout не YAML: %v\n%s", err, r.stdout)
		}
	}
	return r
}

func (r *result) str(key string) string {
	s, _ := r.answer[key].(string)
	return s
}

func (r *result) list(key string) []any {
	l, _ := r.answer[key].([]any)
	return l
}

// request — запрос к подделке GitLab.
type request struct {
	Method, Path, Query, Token string
	Body                       obj
}

// gitlab — подделка GitLab REST API: отвечает тем, что дал тест, запись
// отвечает созданной сущностью, каждый запрос остаётся в requests, а
// опубликованная заметка — в обсуждениях MR. Привязанный к строкам тред
// возвращается DiffNote — так делает GitLab.
type gitlab struct {
	*httptest.Server
	mu          sync.Mutex
	requests    []request
	discussions []any
}

func (g *gitlab) writes() []request {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []request
	for _, r := range g.requests {
		if r.Method != "GET" {
			out = append(out, r)
		}
	}
	return out
}

func (g *gitlab) all() []request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

func startGitlab(t *testing.T, project, iid string, mr obj, changes []obj, versions []obj, discussions []any) *gitlab {
	g := &gitlab{discussions: discussions}
	base := "/api/v4/projects/" + url.PathEscape(project) + "/merge_requests/" + iid
	created := 0
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body obj
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		path := r.URL.EscapedPath()
		g.requests = append(g.requests, request{r.Method, path, r.URL.RawQuery, r.Header.Get("PRIVATE-TOKEN"), body})
		reply := func(status int, data any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if data != nil {
				_ = json.NewEncoder(w).Encode(data)
			}
		}
		if r.Header.Get("PRIVATE-TOKEN") != token {
			reply(401, obj{"message": "401 Unauthorized"})
			return
		}
		sub, ok := strings.CutPrefix(path, base)
		switch {
		case !ok:
			reply(404, obj{"message": "404 Not Found"})
		case r.Method == "GET" && sub == "":
			reply(200, mr)
		case r.Method == "GET" && sub == "/changes":
			reply(200, obj{"changes": changes})
		case r.Method == "GET" && sub == "/versions":
			reply(200, versions)
		case r.Method == "GET" && sub == "/discussions":
			if r.URL.Query().Get("page") == "1" {
				reply(200, g.discussions)
			} else {
				reply(200, []any{})
			}
		case r.Method == "POST" && sub == "/discussions":
			created++
			kind, position := "DiscussionNote", body["position"]
			if position != nil {
				kind = "DiffNote"
			}
			reply(201, obj{"id": fmt.Sprintf("new-%d", created), "notes": []obj{{"id": created, "type": kind, "position": position}}})
		case r.Method == "POST" && sub == "/notes":
			// Заметка остаётся в MR: следующий прогон по той же подделке её прочитает.
			id := 1000 + len(g.requests)
			g.discussions = append(g.discussions, obj{"id": fmt.Sprintf("note-%d", id), "individual_note": true,
				"notes": []obj{{"id": id, "body": body["body"], "author": obj{"username": "bot"}}}})
			reply(201, obj{"id": id})
		case r.Method == "PUT" && strings.HasPrefix(sub, "/discussions/"):
			reply(200, obj{"resolved": true})
		case r.Method == "DELETE" && strings.HasPrefix(sub, "/notes/"):
			reply(204, nil)
		default:
			reply(404, obj{"message": "404 Not Found"})
		}
	}))
	t.Cleanup(g.Close)
	return g
}
