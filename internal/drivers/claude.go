package drivers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/config"
	"github.com/hakastein/llamer/internal/harness"
	"github.com/hakastein/llamer/internal/logx"
)

var claudeAuth = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// Читающие команды Claude Code пропускает сам, а их пишущие формы отбивает:
// выписывать нужно только то, чего он read-only не считает.
var allowedTools = []string{"Read", "Grep", "Glob", "Skill"}

// Всё, чему в ревью нечего делать: их описания едут в каждый промпт, а вызов
// стоит хода. Skill остаётся — им грузится метод этапа.
var disallowedTools = []string{
	"Edit", "Write", "NotebookEdit", "Task", "Agent", "Workflow", "ToolSearch", "ListAgents",
	"ReportFindings", "WebFetch", "WebSearch",
	"TaskCreate", "TaskGet", "TaskList", "TaskOutput", "TaskStop", "TaskUpdate",
	"CronCreate", "CronDelete", "CronList",
	"Monitor", "PushNotification", "RemoteTrigger", "ScheduleWakeup", "SendMessage",
	"DesignSync", "EnterWorktree", "ExitWorktree",
}

// Серверы MCP этапу не даются вовсе: без строгого флага он подхватил бы
// .mcp.json дерева и коннекторы аккаунта.
var noMCP = []string{"--mcp-config", `{"mcpServers":{}}`, "--strict-mcp-config"}

// Память проекта: Claude Code читает AGENTS.md сам, не только через импорт из
// CLAUDE.md, поэтому исключать для тощих этапов нужно оба.
var projectMemoryFiles = []string{"**/CLAUDE.md", "**/CLAUDE.local.md", "**/AGENTS.md"}

// Имя плагина этапа — пространство имён скиллов в Skill: одноимённый скилл
// из `.claude/skills` дерева иначе перехватил бы голое имя.
const stagePlugin = "stage"

// ClaudeHook — скрытая подкоманда llamer, которой Claude Code зовёт хук PreToolUse этапа.
const ClaudeHook = "__claude-hook"

// treeSkillsOff — скиллы дерева из `.claude/skills` Claude Code грузит вместе с
// памятью проекта, и выключаются они только поштучно, по имени.
// shortcut: `.claude/skills` во вложенных каталогах дерева, которые Claude Code
// подхватывает по ходу чтения, не выключаются — выключать, если такие появятся.
func treeSkillsOff(cwd string) map[string]string {
	off := map[string]string{}
	for _, root := range upToRepoRoot(cwd, ".claude/skills") {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if exists(filepath.Join(root, e.Name(), "SKILL.md")) {
				off[e.Name()] = "off"
			}
		}
	}
	return off
}

func claudeConfigDir(env map[string]string) string {
	if d := env["CLAUDE_CONFIG_DIR"]; d != "" {
		return d
	}
	return filepath.Join(homeDir(env), ".claude")
}

// Шаблон со звёздочкой — префикс, и вызов без аргументов ему не отвечает:
// каждая команда разрешается обеими формами. Харнесс разрешён целиком: чужую
// этапу команду он отклоняет сам и называет команды этапа, а отказ Claude Code
// агент читает как запрет Bash вообще и бросает работу.
func bashRules(allow []string) []string {
	var rules []string
	for _, c := range append([]string{harness.Name}, allow...) {
		rules = append(rules, "Bash("+c+")", "Bash("+c+" *)")
	}
	return rules
}

func claudeGaps(settings config.Driver, env map[string]string) []Gap {
	if missing := binaryGap("claude", settings); missing != nil {
		return missing
	}
	credentials := filepath.Join(claudeConfigDir(env), ".credentials.json")
	for _, name := range claudeAuth {
		if env[name] != "" {
			return nil
		}
	}
	if exists(credentials) {
		return nil
	}
	return []Gap{{"auth-claude", "драйверу claude нечем авторизоваться: нет " + credentials +
		" и не задана ни одна из " + strings.Join(claudeAuth, ", ") + " — claude /login"}}
}

type claude struct {
	settings    config.Driver
	env         []string
	runSkills   string
	tmp         string
	configDir   string
	credentials string
	hook        string
}

// prepareClaude — приватный CLAUDE_CONFIG_DIR с копией кредов: личные
// настройки, хуки и плагины разработчика в этап не попадают. Заданный снаружи
// каталог берётся как есть — его готовил запускающий.
func prepareClaude(s Setup) (Driver, error) {
	credentials := filepath.Join(claudeConfigDir(s.Env), ".credentials.json")
	configDir := s.Env["CLAUDE_CONFIG_DIR"]
	if configDir == "" {
		dir, err := os.MkdirTemp(s.Tmp, "claude-")
		if err != nil {
			return nil, err
		}
		configDir = dir
		if exists(credentials) {
			if err := copyFile(credentials, filepath.Join(configDir, ".credentials.json")); err != nil {
				return nil, err
			}
		}
	}
	logx.Logf("драйвер claude: %s, CLAUDE_CONFIG_DIR=%s", s.Settings.Bin, configDir)
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env := append(stageEnvOf(s.Env, s.EnvOrder, s.StageBin, claudeAuth), settingsEnv(s.Settings)...)
	env = append(env,
		// Потолок ожидания фоновых задач снят: верхняя граница и так есть — таймаут этапа.
		"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0",
		// Встроенные скиллы Claude Code этапу не нужны, а их описания едут в каждый промпт.
		"CLAUDE_CODE_DISABLE_BUNDLED_SKILLS=1",
		// Заголовок атрибуции — первый блок системного промпта, и в нём отпечаток
		// задания: префикс кэша расходился бы на первом же блоке.
		"CLAUDE_CODE_ATTRIBUTION_HEADER=0",
		// Кэш промпта на 5 минут вместо часа: запись стоит 1.25 базовой цены
		// вместо 2, а ходы этапа идут с паузой в секунды.
		"FORCE_PROMPT_CACHING_5M=1",
		"CLAUDE_CONFIG_DIR="+configDir,
	)
	return &claude{settings: s.Settings, env: env, runSkills: s.RunSkills, tmp: s.Tmp, configDir: configDir,
		credentials: credentials, hook: shellescape.Quote(exe) + " " + ClaudeHook}, nil
}

func (c *claude) Name() string { return "claude" }

func (c *claude) SkillsHow() string {
	return "Load each of these with the Skill tool before you start."
}

func (c *claude) SkillRef(name string) string { return stagePlugin + ":" + name }

func (c *claude) Quote() string { return `'\''` }

// Launch — задание уходит в stdin, а не аргументом `-p <текст>`: один аргумент
// командной строки в Linux ограничен 128 килобайтами. Общая часть пакета — в
// системном промпте: он у этапов одной модели одинаковый, и кэш, записанный
// первым этапом, читают остальные. Скиллы этапа — его плагин `stage`, скиллы
// дерева выключены.
func (c *claude) Launch(a LaunchArgs) (*agent.Launch, error) {
	// Вызов харнесса длиннее предела разбора Claude Code разрешает хук — см. hook.go.
	hook := map[string]any{"matcher": "Bash", "hooks": []map[string]string{{"type": "command", "command": c.hook}}}
	settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{hook}}, "skillOverrides": treeSkillsOff(a.Cwd)}
	if !a.ProjectMemory {
		settings["claudeMdExcludes"] = projectMemoryFiles
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	plugin := ""
	if c.runSkills != "" {
		plugin = filepath.Join(c.tmp, "claude-plugin-"+a.Stage)
		if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0o777); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+stagePlugin+`"}`+"\n"), 0o666); err != nil {
			return nil, err
		}
		if err := linkSkills(filepath.Join(plugin, "skills"), c.runSkills, a.Skills); err != nil {
			return nil, err
		}
	}
	argv := []string{
		"-p",
		"--setting-sources", "project",
		"--settings", string(settingsJSON),
		// Без dontAsk список ниже ничего не запрещает: в headless всё, что в
		// интерактиве спросило бы подтверждение, молча исполняется.
		"--permission-mode", "dontAsk",
		"--model", a.Level.Model,
		"--effort", a.Level.Effort,
		"--allowedTools", strings.Join(append(slices.Clone(allowedTools), bashRules(a.Allow)...), " "),
		"--disallowedTools", strings.Join(disallowedTools, " "),
	}
	argv = append(argv, noMCP...)
	if plugin != "" {
		argv = append(argv, "--plugin-dir", plugin)
	}
	// Состояние git и окружения Claude Code иначе пишет в системный промпт, и
	// запись артефактов прогона меняла бы его между этапами.
	argv = append(argv, "--exclude-dynamic-system-prompt-sections")
	if a.Shared != nil {
		argv = append(argv, "--append-system-prompt-file", a.Shared.Path)
	}
	argv = append(argv, "--output-format", "stream-json", "--include-partial-messages", "--verbose")
	level := a.Level
	return &agent.Launch{
		Command: c.settings.Bin, Argv: argv, Env: append(slices.Clone(c.env), a.Env...), Input: a.Prompt,
		Model: level.Model, Effort: level.Effort,
		Normalize:   func(line []byte) [][]byte { return [][]byte{line} },
		KeepSession: func(dir, id string) (string, error) { return c.keepSession(dir, id, level) },
	}, nil
}

// keepSession — сессия лежит в projects/<каталог>/<id>.jsonl конфига; по ней же Claude Code её и продолжает.
func (c *claude) keepSession(dir, sessionID string, level config.Level) (string, error) {
	projects := filepath.Join(c.configDir, "projects")
	file := sessionID + ".jsonl"
	entries, _ := os.ReadDir(projects)
	project := ""
	for _, e := range entries {
		if exists(filepath.Join(projects, e.Name(), file)) {
			project = e.Name()
			break
		}
	}
	if project == "" {
		return "", nil
	}
	if err := keepRedacted(filepath.Join(projects, project, file), filepath.Join(dir, "projects", project, file)); err != nil {
		return "", err
	}
	// Вход — из обычного места пользователя ссылкой: копия кредов в артефактах не лежит.
	auth := ""
	if exists(c.credentials) {
		auth = "ln -sf " + shellescape.Quote(c.credentials) + " " + shellescape.Quote(filepath.Join(dir, ".credentials.json")) + " && "
	}
	return auth + "CLAUDE_CONFIG_DIR=" + shellescape.Quote(dir) + " " + c.settings.Bin + " -p --resume " + sessionID +
		" --model " + shellescape.Quote(level.Model) + " --effort " + shellescape.Quote(level.Effort), nil
}
