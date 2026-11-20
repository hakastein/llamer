// Package codex — драйвер этапов Codex CLI.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/drivers"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/pelletier/go-toml/v2"
)

func init() { drivers.Register("codex", driver{}) }

var codexAuth = []string{"CODEX_API_KEY", "OPENAI_API_KEY"}

const rulesFile = "llamer.rules"

// disabled — возможности codex, которые открывали бы агенту что-то кроме команд в песочнице, и снимок
// профиля оболочки: под /bin/sh пользователя он ломает каждую команду.
var disabled = []string{"shell_snapshot", "apps", "plugins", "remote_plugin", "image_generation", "browser_use", "browser_use_external",
	"computer_use", "in_app_browser", "hooks", "multi_agent", "tool_suggest", "skill_mcp_dependency_install", "goals"}

type price struct{ input, cached, cacheWrite, output float64 }

// Долларов за миллион токенов, Standard, запрос до 272K токенов входа:
// https://developers.openai.com/api/docs/pricing. Запрос длиннее целиком стоит
// вдвое за вход и кэш и в полтора раза за выход — там же, на страницах моделей.
// Модели без цены здесь — `$?` в отчёте.
var codexPrices = map[string]price{
	"gpt-6.1-sol":   {2, 0.1, 2.5, 10},
	"gpt-6.1-terra": {1, 0.05, 1.25, 5},
}

const longPrompt = 272_000

// codexUsage — usage запроса или хода codex.
type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

// requestCost — цена одного запроса по его usage; ok=false — цены модели нет.
func requestCost(model string, u codexUsage) (float64, bool) {
	p, ok := codexPrices[model]
	if !ok {
		return 0, false
	}
	input := float64(u.InputTokens-u.CachedInputTokens)*p.input + float64(u.CachedInputTokens)*p.cached + float64(u.CacheWriteInputTokens)*p.cacheWrite
	output := float64(u.OutputTokens) * p.output
	if u.InputTokens > longPrompt {
		return (input*2 + output*1.5) / 1e6, true
	}
	return (input + output) / 1e6, true
}

// sessionRecord — запись журнала сессии codex: в нём то, чего нет в потоке
// `--json`, — usage запросов и лимиты.
type sessionRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type       string          `json:"type"`
		Usage      json.RawMessage `json:"usage"`
		RateLimits *struct {
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
			Reached   *string      `json:"rate_limit_reached_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes *int64  `json:"window_minutes"`
	ResetsAt      *int64  `json:"resets_at"`
}

var rolloutFile = regexp.MustCompile(`rollout-[^/]*\.jsonl$`)

// sessionRecords — записи журналов сессии с концом имени suffix; строка не того вида пропускается.
func sessionRecords(codexHome, suffix string) []sessionRecord {
	var records []sessionRecord
	sessions := filepath.Join(codexHome, "sessions")
	// Каталога сессий нет — этап не дошёл до запроса: записей нет.
	filepath.WalkDir(sessions, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !rolloutFile.MatchString(path) || !strings.HasSuffix(path, suffix) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			var r sessionRecord
			if json.Unmarshal(line, &r) == nil {
				records = append(records, r)
			}
		}
		return nil
	})
	return records
}

// sessionRequests — usage каждого запроса этапа (`token_usage_record`): `--json`
// отдаёт только сумму за ход, а размер контекста и надбавка за длинный промпт
// считаются по запросам.
func sessionRequests(records []sessionRecord) []json.RawMessage {
	requests := []json.RawMessage{}
	for _, r := range records {
		if r.Type == "token_usage_record" && len(r.Payload.Usage) > 0 && string(r.Payload.Usage) != "null" {
			requests = append(requests, r.Payload.Usage)
		}
	}
	return requests
}

var codexWindows = map[int64]string{300: "five_hour", 10080: "seven_day"}

// sessionLimits — лимиты подписки из последней записи `token_count` с
// `rate_limits`, в той же форме, что у claude: окно названо по своей длине в минутах.
func sessionLimits(records []sessionRecord) *agent.Limits {
	for i := len(records) - 1; i >= 0; i-- {
		limits := records[i].Payload.RateLimits
		if records[i].Payload.Type != "token_count" || limits == nil {
			continue
		}
		status := "allowed"
		if limits.Reached != nil && *limits.Reached != "" {
			status = "rejected"
		}
		out := &agent.Limits{Status: &status, Reason: limits.Reached, Windows: map[string]agent.Window{}}
		for _, w := range []*codexWindow{limits.Primary, limits.Secondary} {
			if w == nil {
				continue
			}
			name := "window"
			if w.WindowMinutes != nil {
				name = fmt.Sprintf("%d_minutes", *w.WindowMinutes)
				if known, ok := codexWindows[*w.WindowMinutes]; ok {
					name = known
				}
			}
			out.Windows[name] = agent.Window{UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt}
		}
		return out
	}
	return nil
}

// sessionTotals — пик контекста и цена этапа по его запросам; цена nil, если хоть один не оценить.
func sessionTotals(model string, requests []json.RawMessage) (int64, *float64) {
	var peak int64
	total, known := 0.0, len(requests) > 0
	for _, raw := range requests {
		var u codexUsage
		if json.Unmarshal(raw, &u) != nil {
			known = false
			continue
		}
		peak = max(peak, u.InputTokens)
		c, ok := requestCost(model, u)
		if !ok {
			known = false
		}
		total += c
	}
	if !known {
		return peak, nil
	}
	return peak, &total
}

func codexHomeOf(env map[string]string) string {
	if h := env["CODEX_HOME"]; h != "" {
		return h
	}
	return filepath.Join(drivers.HomeDir(env), ".codex")
}

// quoteTOML — строка TOML в форме JSON: экранирование у них общее, кроме DEL —
// его TOML в базовой строке не пускает.
func quoteTOML(value any) string {
	data, _ := json.Marshal(value)
	return strings.ReplaceAll(string(data), "\x7f", `\u007f`)
}

// rulesFor — разрешённая правилом команда исполняется вне песочницы read-only:
// команды `allow` получают доступ за пределами песочницы (сокет демона, сеть).
func rulesFor(allow []string) string {
	var rules []string
	for _, c := range allow {
		rules = append(rules, "prefix_rule(pattern="+quoteTOML(strings.Split(c, " "))+`, decision="allow")`)
	}
	return strings.Join(rules, "\n") + "\n"
}

// codexEvent — событие `codex exec --json`: только читаемые поля.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Command          string `json:"command"`
		Text             string `json:"text"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
	} `json:"item"`
	Usage   codexUsage      `json:"usage"`
	Error   json.RawMessage `json:"error"`
	Message *string         `json:"message"`
}

type object = map[string]any

func line(event object) []byte {
	data, _ := json.Marshal(event)
	return data
}

// streamUsage — usage итога в строке артефакта: поля и их порядок — как у `modelUsage` claude.
type streamUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// assistant — ход модели из одного блока: content — блок в строке артефакта.
func assistant(content object, b agent.Block) []*agent.Event {
	return []*agent.Event{{Raw: line(object{"type": "assistant", "message": object{"content": []object{content}}}), Kind: "assistant",
		Turn: &agent.Turn{Blocks: []agent.Block{b}}}}
}

// parser — события `codex exec --json` в общей форме. В артефакты этапа они ложатся
// строками в форме потока claude: артефакты обоих драйверов читаются одинаково. Цены,
// usage отдельных запросов и лимитов подписки в потоке нет — их после выхода берёт
// AfterExit из журнала сессии. Строка не JSON не разбирается: её покажет лог.
func parser(model string) func([]byte) ([]*agent.Event, error) {
	lastMessage, turns := "", 0
	return func(raw []byte) ([]*agent.Event, error) {
		if !json.Valid(raw) {
			return nil, errors.New("строка не JSON")
		}
		var e codexEvent
		if json.Unmarshal(raw, &e) != nil {
			return nil, nil
		}
		item := e.Item
		switch e.Type {
		case "thread.started":
			return []*agent.Event{{Raw: line(object{"type": "system", "subtype": "init", "session_id": e.ThreadID}), Kind: "system/init",
				Session: &agent.Session{ID: e.ThreadID}}}, nil
		case "item.started":
			if item.Type == "command_execution" {
				return assistant(object{"type": "tool_use", "id": item.ID, "name": "Bash", "input": object{"command": item.Command}},
					agent.Block{Call: &agent.Call{ID: item.ID, Name: "Bash", Input: object{"command": item.Command}, Command: item.Command}}), nil
			}
		case "item.completed":
			switch item.Type {
			case "agent_message":
				lastMessage = item.Text
				turns++
				return assistant(object{"type": "text", "text": lastMessage}, agent.Block{Text: lastMessage}), nil
			case "reasoning":
				return assistant(object{"type": "thinking", "thinking": item.Text}, agent.Block{Thinking: item.Text}), nil
			case "command_execution":
				failed := item.ExitCode == nil || *item.ExitCode != 0
				return []*agent.Event{{Raw: line(object{"type": "user", "message": object{"content": []object{{"type": "tool_result",
					"tool_use_id": item.ID, "is_error": failed, "content": item.AggregatedOutput}}}}), Kind: "user",
					Replies: []agent.Reply{{CallID: item.ID, Text: item.AggregatedOutput, IsError: failed}}}}, nil
			}
		case "turn.completed":
			u := e.Usage
			usage := streamUsage{InputTokens: u.InputTokens - u.CachedInputTokens, OutputTokens: u.OutputTokens,
				CacheReadInputTokens: u.CachedInputTokens, CacheCreationInputTokens: u.CacheWriteInputTokens}
			return []*agent.Event{{Raw: line(object{"type": "result", "subtype": "success", "is_error": false, "result": lastMessage, "num_turns": turns,
				"total_cost_usd": nil, "modelUsage": object{model: usage}}), Kind: "result/success",
				Result: &agent.Result{Text: lastMessage, Usage: map[string]agent.Usage{model: agent.Usage(usage)}}}}, nil
		case "turn.failed":
			var failure struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &failure) != nil || failure.Message == "" {
				failure.Message = "turn failed"
			}
			return []*agent.Event{{Raw: line(object{"type": "result", "subtype": "error", "is_error": true, "result": failure.Message, "total_cost_usd": nil}),
				Kind: "result/error", Failed: true, Result: &agent.Result{Text: failure.Message, IsError: true}}}, nil
		case "error":
			text := string(raw)
			if e.Message != nil {
				text = *e.Message
			}
			return []*agent.Event{{Raw: line(object{"type": "stage_stderr", "text": text}), Kind: "stage_stderr", Stderr: text}}, nil
		}
		return nil, nil
	}
}
func skillFiles(dir string, depth int) []string {
	if drivers.Exists(filepath.Join(dir, "SKILL.md")) {
		return []string{filepath.Join(dir, "SKILL.md")}
	}
	// shortcut: глубже четырёх уровней скиллы дерева не ищутся — поднять, если codex найдёт там скилл.
	info, err := os.Stat(dir)
	if depth > 3 || err != nil || !info.IsDir() {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, skillFiles(filepath.Join(dir, e.Name()), depth+1)...)
	}
	return out
}

// treeSkills — скиллы дерева: codex сам берёт их из `.codex/skills` и
// `.agents/skills` каждого каталога от cwd до корня репозитория, с вложенными,
// и выключаются они только поштучно, путём до SKILL.md.
func treeSkills(cwd string) []string {
	var roots, out []string
	for _, rel := range []string{".codex/skills", ".agents/skills"} {
		roots = append(roots, drivers.UpToRepoRoot(cwd, rel)...)
	}
	for _, root := range roots {
		out = append(out, skillFiles(root, 0)...)
	}
	return out
}

// driver — драйвер codex до прогона.
type driver struct{}

func (driver) Defaults() drivers.Settings {
	return drivers.Settings{Bin: "codex", Levels: []drivers.Level{
		{Model: "gpt-6.1-sol", Effort: "medium"},
		{Model: "gpt-6.1-sol", Effort: "high"},
		{Model: "gpt-6.1-sol", Effort: "xhigh"},
		{Model: "gpt-6.1-sol", Effort: "max"},
		{Model: "gpt-6.1-sol", Effort: "ultra"},
	}}
}

// Commands — команды llamer codex разрешает правилами, без своих подкоманд: см. rulesFor.
func (driver) Commands() []drivers.Command { return nil }

func (driver) Gaps(ctx context.Context, settings drivers.Settings, env map[string]string) []drivers.Gap {
	if missing := drivers.BinaryGap(ctx, "codex", settings); missing != nil {
		return missing
	}
	auth := filepath.Join(codexHomeOf(env), "auth.json")
	for _, name := range codexAuth {
		if env[name] != "" {
			return nil
		}
	}
	if drivers.Exists(auth) {
		return nil
	}
	return []drivers.Gap{{ID: "auth-codex", Message: "драйверу codex нечем авторизоваться: нет " + auth +
		" и не задана ни одна из " + strings.Join(codexAuth, ", ") + " — codex login"}}
}

// SkillsHow — скиллы этапа codex находит сам в `$HOME/.agents/skills` этапа, а
// упоминание `$имя` в задании подключает скилл целиком ещё до первого хода.
func (driver) SkillsHow() string {
	return "Each of these is attached by its mention below — its text is already with you, follow it from the start."
}

func (driver) SkillRef(name string) string { return "$" + name }

// Quote — не '\”: правило codex не признаёт слово с обратной косой чертой, и вызов ушёл бы в песочницу без записи.
func (driver) Quote() string { return `'"'"'` }

// launcher — драйвер codex, готовый к прогону.
type launcher struct {
	settings  drivers.Settings
	env       []string
	runSkills string
	tmp       string
	auth      string
}

func (driver) Prepare(s drivers.Setup) (drivers.Launcher, error) {
	auth := filepath.Join(codexHomeOf(s.Env), "auth.json")
	from := "из окружения"
	if drivers.Exists(auth) {
		from = auth
	}
	logx.Logf("драйвер codex: %s, вход %s", s.Settings.Bin, from)
	return &launcher{settings: s.Settings, env: s.StageEnv(codexAuth),
		runSkills: s.RunSkills, tmp: s.Tmp, auth: auth}, nil
}

// sandboxed — этап `llamer run`. Песочница — профиль прав codex: чтение только каталога
// контекста, Readable этапа и драйвера, скиллов этапа и HOME этапа, сеть закрыта прокси без
// разрешённых доменов, unix-сокеты открыты. --sandbox read-only сокеты закрывает и профиль не
// применяет. CODEX_HOME общий: копия auth.json разорвала бы цепочку обновления токена входа.
// Его config.toml и правила этап не читает — настройки llamer идут аргументами -c, — а журнал
// сессии codex пишет туда же, и он находится по id сессии.
func (c *launcher) sandboxed(a drivers.LaunchArgs) (*agent.Launch, error) {
	model, effort := a.Level.Model, a.Level.Effort
	home := filepath.Join(c.tmp, "codex-"+a.Stage)
	if err := os.MkdirAll(home, 0o777); err != nil {
		return nil, err
	}
	if c.runSkills != "" {
		if err := drivers.LinkSkills(filepath.Join(home, ".agents", "skills"), c.runSkills, a.Skills); err != nil {
			return nil, err
		}
	}
	readable := []string{`":root" = "deny"`, `":minimal" = "read"`}
	for _, dir := range append(append(a.Sandbox.Paths(a, c.runSkills), home), c.settings.Readable...) {
		readable = append(readable, quoteTOML(dir)+` = "read"`)
	}
	argv := []string{"exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"-C", a.Cwd, "-m", model,
		"-c", "model_reasoning_effort=" + quoteTOML(effort),
		"-c", `web_search="disabled"`,
		"-c", "skills.bundled.enabled=false",
		"-c", "features.network_proxy=true",
		"-c", `default_permissions="llamer"`,
		"-c", `permissions.llamer.extends=":read-only"`,
		"-c", "permissions.llamer.filesystem={" + strings.Join(readable, ", ") + "}",
		"-c", "permissions.llamer.network={enabled = true, dangerously_allow_all_unix_sockets = true}",
		"-c", `approval_policy="never"`, "-c", "allow_login_shell=false", "-c", "project_doc_max_bytes=0",
	}
	if off := treeSkills(a.Cwd); len(off) > 0 {
		var toggles []string
		for _, path := range off {
			toggles = append(toggles, "{path = "+quoteTOML(path)+", enabled = false}")
		}
		argv = append(argv, "-c", "skills.config=["+strings.Join(toggles, ", ")+"]")
	}
	if a.Schema != "" {
		argv = append(argv, "--output-schema", a.Schema)
	}
	for _, f := range disabled {
		argv = append(argv, "--disable", f)
	}
	argv = append(argv, "-")
	codexHome := filepath.Dir(c.auth)
	return &agent.Launch{
		Command: c.settings.Bin, Argv: argv, Env: append(append(slices.Clone(c.env), a.Env...), "HOME="+home, "CODEX_HOME="+codexHome),
		Input: a.Prompt, Model: model, Effort: effort,
		Parse: parser(model),
		AfterExit: func(sessionID string) agent.Late {
			return lateOf(model, sessionRecords(codexHome, "-"+sessionID+".jsonl"))
		},
	}, nil
}

// lateOf — цена, размер контекста, запросы и лимиты этапа по журналу его сессии.
func lateOf(model string, records []sessionRecord) agent.Late {
	requests := sessionRequests(records)
	context, cost := sessionTotals(model, requests)
	return agent.Late{Cost: cost, ContextTokens: context, Requests: requests, Limits: sessionLimits(records)}
}

// Launch — свой HOME и CODEX_HOME на этап: личные скиллы codex берёт из HOME —
// там только скиллы этапа; конфиг и правила — из CODEX_HOME, а правила у этапов
// разные. Журнал сессии остаётся в CODEX_HOME этапа: по нему считаются цена и
// размер контекста. Общая часть пакета — `developer_instructions` в config.toml:
// developer-сообщение поверх базового промпта, и кэш префикса делится между
// этапами. Аргументом `-c` пакет не пройдёт: один аргумент в Linux ограничен 128 килобайтами.
func (c *launcher) Launch(a drivers.LaunchArgs) (*agent.Launch, error) {
	if a.Sandbox != nil {
		return c.sandboxed(a)
	}
	model, effort := a.Level.Model, a.Level.Effort
	home := filepath.Join(c.tmp, "codex-"+a.Stage)
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(codexHome, "rules"), 0o777); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(codexHome, "rules", rulesFile), []byte(rulesFor(a.Allow)), 0o666); err != nil {
		return nil, err
	}
	if drivers.Exists(c.auth) {
		if err := drivers.CopyFile(c.auth, filepath.Join(codexHome, "auth.json")); err != nil {
			return nil, err
		}
	}
	type skillToggle struct {
		Path    string `toml:"path"`
		Enabled bool   `toml:"enabled"`
	}
	var config struct {
		DeveloperInstructions string `toml:"developer_instructions,omitempty"`
		// Login-шелл читает /etc/profile образа и сбрасывает PATH — команды запуска из него пропадают.
		AllowLoginShell bool `toml:"allow_login_shell"`
		Skills          struct {
			Config []skillToggle `toml:"config,omitempty"`
		} `toml:"skills,omitempty"`
	}
	if a.Shared != nil {
		config.DeveloperInstructions = a.Shared.Text
	}
	for _, path := range treeSkills(a.Cwd) {
		config.Skills.Config = append(config.Skills.Config, skillToggle{Path: path})
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), data, 0o666); err != nil {
		return nil, err
	}
	if c.runSkills != "" {
		if err := drivers.LinkSkills(filepath.Join(home, ".agents", "skills"), c.runSkills, a.Skills); err != nil {
			return nil, err
		}
	}
	argv := []string{"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "-c", "skills.bundled.enabled=false",
		"-m", model, "-c", "model_reasoning_effort=" + quoteTOML(effort), "-c", `approval_policy="never"`}
	if !a.ProjectMemory {
		argv = append(argv, "-c", "project_doc_max_bytes=0")
	}
	if a.Schema != "" {
		argv = append(argv, "--output-schema", a.Schema)
	}
	argv = append(argv, "-")
	env := append(append(slices.Clone(c.env), a.Env...), "HOME="+home, "CODEX_HOME="+codexHome)
	return &agent.Launch{
		Command: c.settings.Bin, Argv: argv, Env: env, Input: a.Prompt, Model: model, Effort: effort,
		Parse:     parser(model),
		AfterExit: func(string) agent.Late { return lateOf(model, sessionRecords(codexHome, "")) },
		KeepSession: func(dir, sessionID string) (string, error) {
			for _, name := range []string{"config.toml", "rules", "sessions"} {
				if err := drivers.KeepRedacted(filepath.Join(codexHome, name), filepath.Join(dir, name)); err != nil {
					return "", err
				}
			}
			// Вход — из обычного места пользователя ссылкой: копия кредов в артефактах не лежит.
			auth := ""
			if drivers.Exists(c.auth) {
				auth = "ln -sf " + shellescape.Quote(c.auth) + " " + shellescape.Quote(filepath.Join(dir, "auth.json")) + " && "
			}
			return auth + "CODEX_HOME=" + shellescape.Quote(dir) + " " + c.settings.Bin + " exec resume --skip-git-repo-check -c " +
				shellescape.Quote("model="+quoteTOML(model)) + " -c " + shellescape.Quote("model_reasoning_effort="+quoteTOML(effort)) + " " + sessionID, nil
		},
	}, nil
}
