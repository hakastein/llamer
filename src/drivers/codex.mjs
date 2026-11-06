import {
  copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { HARNESS } from '../agent/stageBin.mjs';
import { parseStreamLine } from '../agent/stream.mjs';
import { StageDriver, binaryGap, stageEnvOf } from './common.mjs';
import { log } from '../log.mjs';

export const CODEX_DEFAULTS = {
  bin: 'codex',
  env: {},
  levels: ['medium', 'high', 'xhigh', 'max', 'ultra'].map((effort) => ({ model: 'gpt-6.1-sol', effort })),
};

export const CODEX_AUTH = ['CODEX_API_KEY', 'OPENAI_API_KEY'];

export const RULES_FILE = 'llamer.rules';

// Долларов за миллион токенов, Standard, запрос до 272K токенов входа:
// https://developers.openai.com/api/docs/pricing. Запрос длиннее целиком стоит
// вдвое за вход и кэш и в полтора раза за выход — там же, на страницах моделей.
// Модели без цены здесь — `$?` в отчёте.
export const CODEX_PRICES = {
  'gpt-6.1-sol': {
    input: 2, cached: 0.1, cacheWrite: 2.5, output: 10,
  },
  'gpt-6.1-terra': {
    input: 1, cached: 0.05, cacheWrite: 1.25, output: 5,
  },
};

const LONG_PROMPT = 272_000;

/** Цена одного запроса по его usage; null — цены модели нет. */
export function requestCost(model, usage) {
  const price = CODEX_PRICES[model];
  if (!price) return null;
  const cached = usage.cached_input_tokens ?? 0;
  const input = ((usage.input_tokens ?? 0) - cached) * price.input + cached * price.cached
    + (usage.cache_write_input_tokens ?? 0) * price.cacheWrite;
  const output = (usage.output_tokens ?? 0) * price.output;
  return ((usage.input_tokens ?? 0) > LONG_PROMPT ? input * 2 + output * 1.5 : input + output) / 1e6;
}

/**
 * Usage каждого запроса этапа из журнала сессии codex (`token_usage_record`):
 * `--json` отдаёт только сумму за ход, а размер контекста и надбавка за длинный
 * промпт считаются по запросам.
 */
export function sessionRequests(codexHome) {
  const sessions = join(codexHome, 'sessions');
  if (!existsSync(sessions)) return [];
  return readdirSync(sessions, { recursive: true })
    .filter((file) => /rollout-[^/]*\.jsonl$/.test(file))
    .flatMap((file) => readFileSync(join(sessions, file), 'utf-8').split('\n').map(parseStreamLine))
    .filter((record) => record?.type === 'token_usage_record' && record.payload?.usage)
    .map((record) => record.payload.usage);
}

/** Пик контекста и цена этапа по его запросам; цена null, если хоть один не оценить. */
export function sessionTotals(model, requests) {
  const costs = requests.map((usage) => requestCost(model, usage));
  return {
    contextTokens: Math.max(0, ...requests.map((usage) => usage.input_tokens ?? 0)),
    cost: costs.length > 0 && !costs.includes(null) ? costs.reduce((sum, cost) => sum + cost, 0) : null,
  };
}

const codexHomeOf = (env) => env.CODEX_HOME || join(homedir(), '.codex');

// Строка TOML в форме JSON: экранирование у них общее, кроме DEL — его TOML в базовой строке не пускает.
const toml = (value) => JSON.stringify(value).replaceAll('\x7f', '\\u007f');

/**
 * Разрешённая правилом команда исполняется вне песочницы read-only: только так
 * харнесс пишет журнал прогона, а команды `allow` ходят туда, куда песочница
 * не пускает (сокет демона, сеть).
 */
export function rulesFor(allow) {
  return [HARNESS, ...allow]
    .map((command) => `prefix_rule(pattern=${toml(command.split(' '))}, decision="allow")`)
    .join('\n').concat('\n');
}

function assistant(content) {
  return { type: 'assistant', message: { content } };
}

function toolResult(id, isError, content) {
  return {
    type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, is_error: isError, content }] },
  };
}

/**
 * События `codex exec --json` в форме потока claude: телеметрия, статистика
 * этапа и разбор срыва читают один формат. Цены и usage отдельных запросов
 * в потоке нет — их после выхода берёт `afterExit` из журнала сессии.
 */
export function codexNormalizer(model) {
  let lastMessage = '';
  let turns = 0;
  return (event) => {
    const { item } = event;
    switch (event.type) {
      case 'item.started':
        if (item?.type === 'command_execution') {
          return [assistant([{ type: 'tool_use', id: item.id, name: 'Bash', input: { command: item.command } }])];
        }
        return [];
      case 'item.completed':
        if (item?.type === 'agent_message') {
          lastMessage = item.text ?? '';
          turns += 1;
          return [assistant([{ type: 'text', text: lastMessage }])];
        }
        if (item?.type === 'reasoning') return [assistant([{ type: 'thinking', thinking: item.text ?? '' }])];
        if (item?.type === 'command_execution') {
          return [toolResult(item.id, item.exit_code !== 0, item.aggregated_output ?? '')];
        }
        return [];
      case 'turn.completed': {
        const usage = event.usage ?? {};
        const cached = usage.cached_input_tokens ?? 0;
        return [{
          type: 'result',
          subtype: 'success',
          is_error: false,
          result: lastMessage,
          num_turns: turns,
          total_cost_usd: null,
          modelUsage: {
            [model]: {
              inputTokens: (usage.input_tokens ?? 0) - cached,
              outputTokens: usage.output_tokens ?? 0,
              cacheReadInputTokens: cached,
              cacheCreationInputTokens: usage.cache_write_input_tokens ?? 0,
            },
          },
        }];
      }
      case 'turn.failed':
        return [{
          type: 'result', subtype: 'error', is_error: true, result: event.error?.message ?? 'turn failed', total_cost_usd: null,
        }];
      case 'error':
        return [{ type: 'stage_stderr', text: event.message ?? JSON.stringify(event) }];
      default:
        return [];
    }
  };
}

export function codexGaps(settings, env) {
  const missing = binaryGap('codex', settings, env);
  if (missing.length > 0) return missing;
  const auth = join(codexHomeOf(env), 'auth.json');
  if (CODEX_AUTH.some((name) => env[name]) || existsSync(auth)) return [];
  return [{
    id: 'auth-codex',
    message: `драйверу codex нечем авторизоваться: нет ${auth}`
      + ` и не задана ни одна из ${CODEX_AUTH.join(', ')} — codex login`,
  }];
}

export class CodexDriver extends StageDriver {
  #env; #runSkills; #tmp; #auth;

  constructor({
    settings, env, runSkills, tmp, auth,
  }) {
    super(settings);
    this.#env = env;
    this.#runSkills = runSkills;
    this.#tmp = tmp;
    this.#auth = auth;
  }

  static prepare({
    settings, env, stageBin, runSkills, tmp,
  }) {
    const auth = join(codexHomeOf(env), 'auth.json');
    log(`драйвер codex: ${settings.bin}, вход ${existsSync(auth) ? auth : 'из окружения'}`);
    return new CodexDriver({
      settings, env: { ...stageEnvOf(env, stageBin, CODEX_AUTH), ...settings.env }, runSkills, tmp, auth,
    });
  }

  get name() { return 'codex'; }

  // Скиллы прогона codex находит сам в `$HOME/.agents/skills` этапа, а упоминание
  // `$имя` в задании подключает скилл целиком ещё до первого хода.
  get skillsHow() { return 'Each of these is attached by its mention below — its text is already with you, follow it from the start.'; }

  skillRef(name) { return `$${name}`; }

  // Не '\'': правило codex не признаёт слово с обратной косой чертой, и вызов ушёл бы в песочницу без записи.
  get quote() { return "'\"'\"'"; }

  /**
   * Свой HOME и CODEX_HOME на этап: личные скиллы codex берёт из HOME — там
   * только скиллы прогона, встроенные отключены; конфиг и правила — из
   * CODEX_HOME, а правила у этапов разные (`allow` этапа). Журнал сессии
   * остаётся в CODEX_HOME этапа: по нему считаются цена и размер контекста.
   * Общая часть пакета — `developer_instructions` в config.toml этого CODEX_HOME:
   * developer-сообщение поверх базового промпта, как системный промпт у claude,
   * и кэш префикса делится между этапами. Аргументом `-c` пакет не пройдёт:
   * один аргумент в Linux ограничен 128 килобайтами.
   */
  launch({
    stage, level, projectMemory, allow, shared, prompt, env,
  }) {
    const { model, effort } = level;
    const home = join(this.#tmp, `codex-${stage}`);
    const codexHome = join(home, '.codex');
    mkdirSync(join(codexHome, 'rules'), { recursive: true });
    writeFileSync(join(codexHome, 'rules', RULES_FILE), rulesFor(allow));
    if (existsSync(this.#auth)) copyFileSync(this.#auth, join(codexHome, 'auth.json'));
    writeFileSync(join(codexHome, 'config.toml'), shared ? `developer_instructions = ${toml(shared.text)}\n` : '');
    const skills = join(home, '.agents', 'skills');
    if (this.#runSkills && !existsSync(skills)) {
      mkdirSync(join(home, '.agents'), { recursive: true });
      symlinkSync(join(this.#runSkills, 'skills'), skills);
    }
    return {
      command: this.settings.bin,
      argv: [
        'exec', '--json',
        '--sandbox', 'read-only',
        '--skip-git-repo-check',
        '-c', 'skills.bundled.enabled=false',
        '-m', model,
        '-c', `model_reasoning_effort=${toml(effort)}`,
        '-c', 'approval_policy="never"',
        ...(projectMemory ? [] : ['-c', 'project_doc_max_bytes=0']),
        '-',
      ],
      env: {
        ...this.#env, ...env, HOME: home, CODEX_HOME: codexHome,
      },
      input: prompt,
      model,
      effort,
      normalize: codexNormalizer(model),
      afterExit: () => sessionTotals(model, sessionRequests(codexHome)),
    };
  }
}
