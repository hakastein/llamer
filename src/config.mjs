import { existsSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { DRIVER_DEFAULTS } from './drivers/index.mjs';

export const CONFIG_FILE = '.llamer.json';

export const CONFIG_VERSION = 1;

/** Уровней глубины пять: оценщик выбирает номер, этап идёт моделью этого уровня на лестнице своего драйвера. */
export const LEVEL_COUNT = 5;

export const ASSESSED = 'assessed';

export const SERVICE_STAGES = ['assess', 'dedupe', 'skeptic', 'threads'];

/**
 * Встроенные этапы идут всегда. Задание каждого — в его шаблоне; `title`
 * встроенного разбирающего этапа — то, как его называют дедупликация и скептик.
 * Скиллы — из `skills/` пакета; методу тестов и комментариев своего скилла в
 * пакете нет, его дают конфигом. `paths` — маски по умолчанию.
 */
export const BUILTIN_STAGES = {
  assess: { skills: ['assessing-review-scope'], level: 1 },
  threads: { skills: [], level: 1 },
  logic: { skills: ['reviewing-business-logic'], level: ASSESSED, title: 'the business-logic reviewer' },
  quality: { skills: ['reviewing-code-quality'], level: 2, title: 'the code-quality reviewer' },
  comments: { skills: [], level: 1, title: 'the comments reviewer' },
  tests: { skills: [], level: 1, title: 'the test reviewer', paths: ['**/tests/**'] },
  dedupe: { skills: [], level: 1 },
  skeptic: { skills: ['refuting-review-findings'], level: ASSESSED },
};

// Оценщик и треды идут до вердикта: уровня от оценщика у них ещё нет.
const BEFORE_ASSESSMENT = ['assess', 'threads'];

const ROOT_KEYS = ['version', 'skillPaths', 'allow', 'prompt', 'drivers', 'tracker', 'sharedBranches', 'stages'];
const BUILTIN_KEYS = ['drivers', 'skills', 'level', 'allow', 'prompt'];
const BUILTIN_ANALYSIS_KEYS = [...BUILTIN_KEYS, 'paths'];
const STAGE_KEYS = ['task', 'drivers', 'skills', 'level', 'paths', 'allow', 'prompt'];
const PROMPT_KEYS = ['protocol', 'plan'];
const SKILLS_KEYS = ['always', 'onDemand'];
const DRIVER_KEYS = ['bin', 'env', 'levels'];
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
  const vars = value.env === undefined ? {} : read.object(value.env, `drivers.${name}.env`, Object.keys(Object(value.env)));
  const env = {};
  for (const [key, text] of Object.entries(vars)) {
    const path = `drivers.${name}.env.${key}`;
    env[key] = expandVars(read.string(text, path), { env: processEnv, read, path });
  }
  return {
    bin: value.bin === undefined ? defaults.bin : read.string(value.bin, `drivers.${name}.bin`),
    env: { ...defaults.env, ...env },
    levels: parseLevels(read, value.levels, `drivers.${name}.levels`, defaults.levels),
  };
}

// Лестница своя у каждого драйвера: номер уровня один, модели у драйверов разные.
function parseLevels(read, raw, path, fallback) {
  if (raw === undefined) return fallback;
  if (!Array.isArray(raw) || raw.length !== LEVEL_COUNT) {
    read.fail(path, `должен быть списком из ${LEVEL_COUNT} уровней`);
  }
  return raw.map((level, index) => {
    const at = `${path}[${index}]`;
    read.object(level, at, LEVEL_KEYS);
    return {
      model: read.string(level.model, `${at}.model`),
      effort: read.string(level.effort, `${at}.effort`),
    };
  });
}

/**
 * Драйверы этапа — явно у каждого, у встроенного тоже: умолчание выбрало бы
 * за проект, на чьи деньги идёт этап. Служебный этап — один на прогон.
 */
function parseStageDrivers(read, raw, path, key) {
  const names = Object.keys(DRIVER_DEFAULTS);
  if (!Array.isArray(raw) || raw.length === 0) {
    read.fail(path, `не задан — у каждого этапа, и у встроенного, драйверы задаются явно: непустой список из ${names.join(', ')}`);
  }
  const drivers = raw.map((name, index) => read.oneOf(name, `${path}[${index}]`, names));
  const repeated = drivers.filter((name, index) => drivers.indexOf(name) !== index);
  if (repeated.length > 0) read.fail(path, `драйвер повторяется: ${repeated.join(', ')}`);
  if (SERVICE_STAGES.includes(key) && drivers.length !== 1) {
    read.fail(path, `${key} — служебный этап, он идёт одним экземпляром: ровно один драйвер`);
  }
  return drivers;
}

function parseLevel(read, value, path, fallback) {
  if (value === undefined) return fallback;
  if (value === ASSESSED) return value;
  if (!Number.isInteger(value) || value < 1 || value > LEVEL_COUNT) {
    read.fail(path, `«${ASSESSED}» или число от 1 до ${LEVEL_COUNT}`);
  }
  return value;
}

// Встроенные скиллы этапа (`base`) идут в always: убрать их конфигом нельзя.
function parseSkills(read, raw, path, base) {
  const value = read.object(raw, path, SKILLS_KEYS);
  const always = [...new Set([...base, ...read.strings(value.always, `${path}.always`)])];
  const onDemand = read.strings(value.onDemand, `${path}.onDemand`);
  const both = always.filter((name) => onDemand.includes(name));
  if (both.length > 0) read.fail(path, `скилл в обоих списках, always и onDemand: ${both.join(', ')}`);
  return { always, onDemand };
}

/**
 * Строки задания, которые знает проект, а не движок: `protocol` дописывается в
 * «How This Run Works», `plan` заменяет план по умолчанию (поиск по тексту).
 * Так проект говорит этапу о навигации, которую сам поднял и разрешил в `allow`.
 */
function parsePrompt(read, raw, path) {
  if (raw === undefined) return {};
  const value = read.object(raw, path, PROMPT_KEYS);
  return Object.fromEntries(PROMPT_KEYS.filter((key) => value[key] !== undefined)
    .map((key) => [key, read.string(value[key], `${path}.${key}`)]));
}

/**
 * Этап конфига поверх встроенного: встроенному добавляются скиллы, задаются
 * уровень, `allow` и `prompt`, разбирающему — ещё маски; свой этап несёт задачу
 * и маски путей. `prompt` этапа перекрывает общий по ключам.
 */
function parseStage(read, key, raw, prompt) {
  const path = `stages.${key}`;
  const builtin = BUILTIN_STAGES[key];
  const keys = builtin ? (builtin.title ? BUILTIN_ANALYSIS_KEYS : BUILTIN_KEYS) : STAGE_KEYS;
  const stage = read.object(raw, path, keys);
  if (!builtin && !STAGE_ID.test(key)) read.fail(path, `имя этапа не подходит под ${STAGE_ID}`);
  const level = parseLevel(read, stage.level, `${path}.level`, builtin?.level ?? ASSESSED);
  if (level === ASSESSED && BEFORE_ASSESSMENT.includes(key)) {
    read.fail(`${path}.level`, `у ${key} — только число от 1 до ${LEVEL_COUNT}`);
  }
  return {
    key,
    title: builtin ? builtin.title ?? null : `the ${key} stage`,
    task: builtin ? null : read.string(stage.task, `${path}.task`),
    drivers: parseStageDrivers(read, stage.drivers, `${path}.drivers`, key),
    skills: parseSkills(read, stage.skills ?? {}, `${path}.skills`, builtin?.skills ?? []),
    paths: stage.paths === undefined ? builtin?.paths ?? [] : read.strings(stage.paths, `${path}.paths`),
    level,
    allow: read.strings(stage.allow, `${path}.allow`, COMMAND),
    prompt: { ...prompt, ...parsePrompt(read, stage.prompt, `${path}.prompt`) },
  };
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

  const prompt = parsePrompt(read, config.prompt, 'prompt');
  const own = config.stages === undefined ? {} : read.object(config.stages, 'stages', Object.keys(Object(config.stages)));
  const stages = {};
  for (const key of new Set([...Object.keys(BUILTIN_STAGES), ...Object.keys(own)])) {
    stages[key] = parseStage(read, key, Object.hasOwn(own, key) ? own[key] : {}, prompt);
  }

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

/** Разбирающие этапы: все, кроме служебных, — встроенные первыми. */
export function analysisStages(config) {
  return Object.values(config.stages).filter((stage) => !SERVICE_STAGES.includes(stage.key));
}

/** Драйверы всех этапов конфига. */
export function driversInUse(config) {
  return [...new Set(Object.values(config.stages).flatMap((stage) => stage.drivers))];
}

/** Имя экземпляра этапа: этап на нескольких драйверах идёт экземпляром на каждый, `logic·codex`. */
export function instanceName(stage, driver) {
  return stage.drivers.length > 1 ? `${stage.key}·${driver}` : stage.key;
}

/** Скиллы всех этапов конфига, обоих списков. */
export function skillsInUse(config) {
  return [...new Set(Object.values(config.stages).flatMap((stage) => [...stage.skills.always, ...stage.skills.onDemand]))];
}
