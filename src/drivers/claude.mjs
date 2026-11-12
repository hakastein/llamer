import {
  copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { HARNESS, quoted } from '../agent/stageBin.mjs';
import {
  StageDriver, binaryGap, keepRedacted, linkSkills, stageEnvOf, upToRepoRoot,
} from './common.mjs';
import { log } from '../log.mjs';

export const CLAUDE_DEFAULTS = {
  bin: 'claude',
  env: {},
  levels: [
    { model: 'claude-sonnet-5-5', effort: 'medium' },
    { model: 'claude-sonnet-5-5', effort: 'high' },
    { model: 'claude-opus-5-5', effort: 'low' },
    { model: 'claude-opus-5-5', effort: 'medium' },
    { model: 'claude-opus-5-5', effort: 'high' },
  ],
};

export const CLAUDE_AUTH = ['CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY', 'ANTHROPIC_AUTH_TOKEN'];

// Читающие команды Claude Code пропускает сам, а их пишущие формы отбивает:
// выписывать нужно только то, чего он read-only не считает.
const ALLOWED_TOOLS = ['Read', 'Grep', 'Glob', 'Skill'];

// Всё, чему в ревью нечего делать: их описания едут в каждый промпт, а вызов
// стоит хода. Skill остаётся — им грузится метод этапа.
const DISALLOWED_TOOLS = [
  'Edit', 'Write', 'NotebookEdit', 'Task', 'Agent', 'Workflow', 'ToolSearch', 'ListAgents',
  'ReportFindings', 'WebFetch', 'WebSearch',
  'TaskCreate', 'TaskGet', 'TaskList', 'TaskOutput', 'TaskStop', 'TaskUpdate',
  'CronCreate', 'CronDelete', 'CronList',
  'Monitor', 'PushNotification', 'RemoteTrigger', 'ScheduleWakeup', 'SendMessage',
  'DesignSync', 'EnterWorktree', 'ExitWorktree',
];

// Серверы MCP этапу не даются вовсе: без строгого флага он подхватил бы
// .mcp.json дерева и коннекторы аккаунта.
const NO_MCP = ['--mcp-config', JSON.stringify({ mcpServers: {} }), '--strict-mcp-config'];

// Память проекта: Claude Code читает AGENTS.md сам, не только через импорт из
// CLAUDE.md, поэтому исключать для тощих этапов нужно оба.
const PROJECT_MEMORY_FILES = ['**/CLAUDE.md', '**/CLAUDE.local.md', '**/AGENTS.md'];

// Имя плагина этапа — пространство имён скиллов в Skill: одноимённый скилл
// из `.claude/skills` дерева иначе перехватил бы голое имя.
const STAGE_PLUGIN = 'stage';

/**
 * Скиллы дерева из `.claude/skills` Claude Code грузит вместе с памятью проекта
 * (`--setting-sources project`), и выключаются они только поштучно, по имени.
 * shortcut: `.claude/skills` во вложенных каталогах дерева, которые Claude Code
 * подхватывает по ходу чтения, не выключаются — выключать, если такие появятся.
 */
function treeSkillsOff(cwd) {
  const names = upToRepoRoot(cwd, '.claude/skills')
    .flatMap((root) => readdirSync(root).filter((name) => existsSync(join(root, name, 'SKILL.md'))));
  return Object.fromEntries(names.map((name) => [name, 'off']));
}

// Вызов харнесса длиннее предела разбора Claude Code разрешает хук — см. claudeHook.mjs.
const HARNESS_HOOK = {
  PreToolUse: [{
    matcher: 'Bash',
    hooks: [{
      type: 'command',
      command: `${quoted(process.execPath)} ${quoted(fileURLToPath(new URL('./claudeHook.mjs', import.meta.url)))}`,
    }],
  }],
};

const configDirOf = (env) => env.CLAUDE_CONFIG_DIR || join(homedir(), '.claude');

// Шаблон со звёздочкой — префикс, и вызов без аргументов ему не отвечает:
// каждая команда разрешается обеими формами. Харнесс разрешён целиком: чужую
// этапу команду он отклоняет сам и называет команды этапа, а отказ Claude Code
// агент читает как запрет Bash вообще и бросает работу.
function bashRules(allow) {
  return [HARNESS, ...allow].flatMap((command) => [`Bash(${command})`, `Bash(${command} *)`]);
}

export function claudeGaps(settings, env) {
  const missing = binaryGap('claude', settings, env);
  if (missing.length > 0) return missing;
  const credentials = join(configDirOf(env), '.credentials.json');
  if (CLAUDE_AUTH.some((name) => env[name]) || existsSync(credentials)) return [];
  return [{
    id: 'auth-claude',
    message: `драйверу claude нечем авторизоваться: нет ${credentials}`
      + ` и не задана ни одна из ${CLAUDE_AUTH.join(', ')} — claude /login`,
  }];
}

export class ClaudeDriver extends StageDriver {
  #env; #runSkills; #tmp; #configDir; #credentials;

  constructor({
    settings, env, runSkills, tmp, credentials,
  }) {
    super(settings);
    this.#env = env;
    this.#runSkills = runSkills;
    this.#tmp = tmp;
    this.#configDir = env.CLAUDE_CONFIG_DIR;
    this.#credentials = credentials;
  }

  /**
   * Приватный CLAUDE_CONFIG_DIR с копией кредов: личные настройки, хуки и
   * плагины разработчика в этап не попадают. Заданный снаружи каталог берётся
   * как есть — его готовил запускающий.
   */
  static prepare({
    settings, env, stageBin, runSkills, tmp,
  }) {
    const credentials = join(configDirOf(env), '.credentials.json');
    let configDir = env.CLAUDE_CONFIG_DIR;
    if (!configDir) {
      configDir = mkdtempSync(join(tmp, 'claude-'));
      if (existsSync(credentials)) copyFileSync(credentials, join(configDir, '.credentials.json'));
    }
    log(`драйвер claude: ${settings.bin}, CLAUDE_CONFIG_DIR=${configDir}`);
    return new ClaudeDriver({
      settings,
      runSkills,
      tmp,
      credentials,
      env: {
        ...stageEnvOf(env, stageBin, CLAUDE_AUTH),
        ...settings.env,
        // Потолок ожидания фоновых задач снят: верхняя граница и так есть —
        // таймаут этапа.
        CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS: '0',
        // Встроенные скиллы Claude Code этапу не нужны, а их описания едут в каждый промпт.
        CLAUDE_CODE_DISABLE_BUNDLED_SKILLS: '1',
        // Заголовок атрибуции — первый блок системного промпта, и в нём отпечаток
        // задания. У этапов задания разные, и префикс кэша расходился бы на первом
        // же блоке: общая часть пакета не делилась бы вовсе.
        CLAUDE_CODE_ATTRIBUTION_HEADER: '0',
        // Кэш промпта на 5 минут вместо часа: запись стоит 1.25 базовой цены
        // вместо 2, а ходы этапа идут с паузой в секунды.
        FORCE_PROMPT_CACHING_5M: '1',
        CLAUDE_CONFIG_DIR: configDir,
      },
    });
  }

  get name() { return 'claude'; }

  get skillsHow() { return 'Load each of these with the Skill tool before you start.'; }

  skillRef(name) { return `${STAGE_PLUGIN}:${name}`; }

  /**
   * Задание уходит в stdin, а не аргументом `-p <текст>`: один аргумент
   * командной строки в Linux ограничен 128 килобайтами, и пакет контекста ронял
   * бы запуск на `spawn E2BIG`. Общая часть пакета — в системном промпте: он у
   * этапов одной модели одинаковый, и кэш, записанный первым этапом, читают
   * остальные. Скиллы этапа — его плагин `stage`, скиллы дерева выключены.
   */
  launch({
    stage, level, projectMemory, allow, shared, prompt, env, skills, cwd,
  }) {
    const { model, effort } = level;
    const settings = {
      hooks: HARNESS_HOOK,
      skillOverrides: treeSkillsOff(cwd),
      ...(projectMemory ? {} : { claudeMdExcludes: PROJECT_MEMORY_FILES }),
    };
    const plugin = this.#runSkills ? join(this.#tmp, `claude-plugin-${stage}`) : null;
    if (plugin) {
      mkdirSync(join(plugin, '.claude-plugin'), { recursive: true });
      writeFileSync(join(plugin, '.claude-plugin', 'plugin.json'), `${JSON.stringify({ name: STAGE_PLUGIN })}\n`);
      linkSkills(join(plugin, 'skills'), this.#runSkills, skills);
    }
    return {
      command: this.settings.bin,
      argv: [
        '-p',
        '--setting-sources', 'project',
        '--settings', JSON.stringify(settings),
        // Без dontAsk список ниже ничего не запрещает: в headless всё, что в
        // интерактиве спросило бы подтверждение, молча исполняется.
        '--permission-mode', 'dontAsk',
        '--model', model,
        '--effort', effort,
        '--allowedTools', [...ALLOWED_TOOLS, ...bashRules(allow)].join(' '),
        '--disallowedTools', DISALLOWED_TOOLS.join(' '),
        ...NO_MCP,
        ...(plugin ? ['--plugin-dir', plugin] : []),
        // Состояние git и окружения Claude Code иначе пишет в системный промпт,
        // и запись артефактов прогона меняла бы его между этапами.
        '--exclude-dynamic-system-prompt-sections',
        ...(shared ? ['--append-system-prompt-file', shared.path] : []),
        '--output-format', 'stream-json',
        '--include-partial-messages',
        '--verbose',
      ],
      env: { ...this.#env, ...env },
      input: prompt,
      model,
      effort,
      normalize: (event) => [event],
      keepSession: (dir, sessionId) => this.#keepSession(dir, sessionId, level),
    };
  }

  // Сессия лежит в projects/<каталог>/<id>.jsonl конфига; по ней же Claude Code её и продолжает.
  #keepSession(dir, sessionId, { model, effort }) {
    const projects = join(this.#configDir, 'projects');
    const file = `${sessionId}.jsonl`;
    const project = existsSync(projects) && readdirSync(projects).find((name) => existsSync(join(projects, name, file)));
    if (!project) return null;
    keepRedacted(join(projects, project, file), join(dir, 'projects', project, file));
    // Вход — из обычного места пользователя ссылкой: копия кредов в артефактах не лежит.
    const auth = existsSync(this.#credentials)
      ? `ln -sf ${quoted(this.#credentials)} ${quoted(join(dir, '.credentials.json'))} && ` : '';
    return `${auth}CLAUDE_CONFIG_DIR=${quoted(dir)} ${this.settings.bin} -p --resume ${sessionId}`
      + ` --model ${quoted(model)} --effort ${quoted(effort)}`;
  }
}
