import {
  copyFileSync, existsSync, mkdirSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { HARNESS } from '../agent/stageBin.mjs';
import { StageDriver, binaryGap, stageEnvOf } from './common.mjs';
import { log } from '../log.mjs';

export const CODEX_DEFAULTS = {
  bin: 'codex',
  env: {},
};

export const CODEX_AUTH = ['CODEX_API_KEY', 'OPENAI_API_KEY'];

export const RULES_FILE = 'llamer.rules';

const codexHomeOf = (env) => env.CODEX_HOME || join(homedir(), '.codex');

const toml = (value) => JSON.stringify(value);

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
 * этапа и разбор срыва читают один формат. Цены codex в потоке не отдаёт —
 * `total_cost_usd` остаётся неизвестным, токены считаются.
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

  // Инструмента Skill у Codex нет — метод этапа он читает файлом из каталога скиллов прогона.
  get skillsHow() { return 'Прочитай каждый из этих файлов целиком до начала работы.'; }

  get onDemandHow() {
    return 'Прочитай целиком те из этих файлов, чьё описание подходит к файлам реквеста; остальные не читай.';
  }

  skillRef(name) { return join(this.#runSkills, 'skills', name, 'SKILL.md'); }

  /**
   * Свой HOME и CODEX_HOME на этап: личные скиллы codex берёт из HOME, конфиг и
   * правила — из CODEX_HOME, а правила у этапов разные (`allow` этапа).
   * Общая часть пакета идёт началом stdin: кэш префикса делится между этапами.
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
    return {
      command: this.settings.bin,
      argv: [
        'exec', '--json',
        '--sandbox', 'read-only',
        '--skip-git-repo-check',
        '--ignore-user-config',
        '--ephemeral',
        '-m', model,
        '-c', `model_reasoning_effort=${toml(effort)}`,
        '-c', 'approval_policy="never"',
        ...(projectMemory ? [] : ['-c', 'project_doc_max_bytes=0']),
        '-',
      ],
      env: {
        ...this.#env, ...env, HOME: home, CODEX_HOME: codexHome,
      },
      input: shared ? `${shared.text}\n\n${prompt}` : prompt,
      model,
      effort,
      normalize: codexNormalizer(model),
    };
  }
}
