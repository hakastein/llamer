import { existsSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { DRIVER_DEFAULTS } from './drivers/index.mjs';

export const CONFIG_FILE = '.llamer.json';

export const CONFIG_VERSION = 1;

/** Ступеней глубины столько, сколько уровней у драйвера: оценщик выбирает номер. */
export const LEVEL_COUNT = 5;

export const SERVICE_STAGES = ['assess', 'dedupe', 'skeptic', 'threads'];

export const ASSESSED = 'assessed';

const ROOT_KEYS = ['version', 'skillPaths', 'allow', 'drivers', 'tracker', 'sharedBranches', 'service', 'stages'];
const STAGE_KEYS = ['id', 'skills', 'mandate', 'paths', 'driver', 'level', 'allow'];
const SERVICE_KEYS = ['skills', 'driver'];
const DRIVER_KEYS = ['bin', 'levels', 'env'];
const LEVEL_KEYS = ['model', 'effort'];
const TRACKER_KEYS = ['type', 'issuePattern'];
const TRACKERS = ['youtrack', 'none'];

// Команда — слова без метасимволов оболочки: `rg`, `git log`. Разрешение
// префиксное, и `;` или `|` в нём разрешили бы цепочку чего угодно.
const COMMAND = /^[\w./:@=+-]+( [\w./:@=+-]+)*$/;
const STAGE_ID = /^[a-z][a-z0-9-]*$/;

export class ConfigError extends Error {}

class Reader {
  #file;

  constructor(file) {
    this.#file = file;
  }

  fail(path, message) {
    throw new ConfigError(`${this.#file}: ${path ? `${path}: ` : ''}${message}`);
  }

  // Опечатка в ключе иначе молча выключала бы то, что человек думал включить.
  object(value, path, keys) {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) this.fail(path, 'должен быть объектом');
    const unknown = Object.keys(value).filter((key) => !keys.includes(key));
    if (unknown.length > 0) this.fail(path, `неизвестные ключи: ${unknown.join(', ')}; допустимы ${keys.join(', ')}`);
    return value;
  }

  string(value, path) {
    if (typeof value !== 'string' || value.trim() === '') this.fail(path, 'должен быть непустой строкой');
    return value;
  }

  strings(value, path, check = null) {
    if (value === undefined) return [];
    if (!Array.isArray(value)) this.fail(path, 'должен быть списком строк');
    return value.map((item, index) => {
      const text = this.string(item, `${path}[${index}]`);
      if (check && !check.test(text)) this.fail(`${path}[${index}]`, `«${text}» не подходит под ${check}`);
      return text;
    });
  }

  oneOf(value, path, allowed) {
    if (!allowed.includes(value)) this.fail(path, `одно из ${allowed.join(', ')}; пришло «${value}»`);
    return value;
  }
}

// `${VAR}` и `~` раскрываются: каталог скиллов у CI и у разработчика разный,
// а конфиг один на репозиторий. `${VAR}` — и в значениях drivers.<имя>.env.
function expandVars(raw, { env, read, path }) {
  return raw.replace(/\$\{(\w+)\}/g, (_, name) => env[name] ?? read.fail(path, `переменная ${name} не задана`));
}

function expandPath(raw, { env, base, read, path }) {
  return resolve(base, expandVars(raw, { env, read, path }).replace(/^~(?=\/|$)/, homedir()));
}

function parseDriver(read, name, raw, processEnv) {
  const defaults = DRIVER_DEFAULTS[name];
  const value = read.object(raw ?? {}, `drivers.${name}`, DRIVER_KEYS);
  let { levels } = defaults;
  if (value.levels !== undefined) {
    if (!Array.isArray(value.levels) || value.levels.length !== LEVEL_COUNT) {
      read.fail(`drivers.${name}.levels`, `должен быть списком из ${LEVEL_COUNT} уровней`);
    }
    levels = value.levels.map((level, index) => {
      const path = `drivers.${name}.levels[${index}]`;
      read.object(level, path, LEVEL_KEYS);
      return { model: read.string(level.model, `${path}.model`), effort: read.string(level.effort, `${path}.effort`) };
    });
  }
  const vars = value.env === undefined ? {} : read.object(value.env, `drivers.${name}.env`, Object.keys(Object(value.env)));
  const env = {};
  for (const [key, text] of Object.entries(vars)) {
    const path = `drivers.${name}.env.${key}`;
    env[key] = expandVars(read.string(text, path), { env: processEnv, read, path });
  }
  return {
    bin: value.bin === undefined ? defaults.bin : read.string(value.bin, `drivers.${name}.bin`),
    levels,
    env: { ...defaults.env, ...env },
  };
}

function parseLevel(read, value, path) {
  if (value === undefined || value === ASSESSED) return ASSESSED;
  if (!Number.isInteger(value) || value < 1 || value > LEVEL_COUNT) {
    read.fail(path, `«${ASSESSED}» или число от 1 до ${LEVEL_COUNT}`);
  }
  return value;
}

function parseTracker(read, raw) {
  if (raw === undefined) return { type: 'none', issuePattern: null };
  const value = read.object(raw, 'tracker', TRACKER_KEYS);
  const type = read.oneOf(value.type, 'tracker.type', TRACKERS);
  if (type === 'none') return { type, issuePattern: null };
  const issuePattern = read.string(value.issuePattern, 'tracker.issuePattern');
  try {
    new RegExp(issuePattern, 'i');
  } catch (error) {
    read.fail('tracker.issuePattern', `не собирается в регулярное выражение: ${error.message}`);
  }
  return { type, issuePattern };
}

/**
 * Конфиг проекта. Версия и каждый ключ проверяются до прогона: незнакомое
 * останавливает его, а не выключает молча то, что человек думал включить.
 */
export function parseConfig(raw, { file, env = process.env }) {
  const read = new Reader(file);
  const config = read.object(raw, '', ROOT_KEYS);
  if (config.version !== CONFIG_VERSION) {
    read.fail('version', `поддерживается ${CONFIG_VERSION}; пришло «${config.version}»`);
  }

  if (config.drivers !== undefined) read.object(config.drivers, 'drivers', Object.keys(DRIVER_DEFAULTS));
  const drivers = {};
  for (const name of Object.keys(DRIVER_DEFAULTS)) drivers[name] = parseDriver(read, name, config.drivers?.[name], env);
  const driverOf = (value, path) => (value === undefined ? 'claude' : read.oneOf(value, path, Object.keys(drivers)));

  const service = {};
  if (config.service !== undefined) read.object(config.service, 'service', SERVICE_STAGES);
  for (const key of SERVICE_STAGES) {
    const value = read.object(config.service?.[key] ?? {}, `service.${key}`, SERVICE_KEYS);
    service[key] = {
      skills: read.strings(value.skills, `service.${key}.skills`),
      driver: driverOf(value.driver, `service.${key}.driver`),
    };
  }

  if (!Array.isArray(config.stages) || config.stages.length === 0) {
    read.fail('stages', 'нужен хотя бы один разбирающий этап');
  }
  const ids = new Set(SERVICE_STAGES);
  const stages = config.stages.map((rawStage, index) => {
    const path = `stages[${index}]`;
    const stage = read.object(rawStage, path, STAGE_KEYS);
    const id = read.string(stage.id, `${path}.id`);
    if (!STAGE_ID.test(id)) read.fail(`${path}.id`, `«${id}» не подходит под ${STAGE_ID}`);
    if (ids.has(id)) read.fail(`${path}.id`, `«${id}» уже занят${SERVICE_STAGES.includes(id) ? ' служебным этапом' : ''}`);
    ids.add(id);
    return {
      id,
      skills: read.strings(stage.skills, `${path}.skills`),
      mandate: read.string(stage.mandate, `${path}.mandate`),
      paths: read.strings(stage.paths, `${path}.paths`),
      driver: driverOf(stage.driver, `${path}.driver`),
      level: parseLevel(read, stage.level, `${path}.level`),
      allow: read.strings(stage.allow, `${path}.allow`, COMMAND),
    };
  });

  const base = dirname(file);
  return {
    source: file,
    skillPaths: read.strings(config.skillPaths, 'skillPaths')
      .map((path, index) => expandPath(path, {
        env, base, read, path: `skillPaths[${index}]`,
      })),
    allow: read.strings(config.allow, 'allow', COMMAND),
    drivers,
    tracker: parseTracker(read, config.tracker),
    sharedBranches: read.strings(config.sharedBranches, 'sharedBranches'),
    service,
    stages,
  };
}

export function loadConfig(file, env = process.env) {
  if (!existsSync(file)) throw new ConfigError(`конфиг ${file} не найден — положи ${CONFIG_FILE} в корень проекта или передай --config`);
  let raw;
  try {
    raw = JSON.parse(readFileSync(file, 'utf-8'));
  } catch (error) {
    throw new ConfigError(`${file}: не читается как JSON (${error.message})`);
  }
  return parseConfig(raw, { file, env });
}

/** Драйверы, которыми идут этапы этого конфига. */
export function driversInUse(config) {
  return [...new Set([...config.stages, ...Object.values(config.service)].map((stage) => stage.driver))];
}

/** Скиллы всех этапов конфига. */
export function skillsInUse(config) {
  return [...new Set([...config.stages, ...Object.values(config.service)].flatMap((stage) => stage.skills))];
}
