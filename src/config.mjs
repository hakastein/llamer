import { existsSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { DRIVER_DEFAULTS } from './drivers/index.mjs';

export const CONFIG_FILE = '.llamer.json';

export const CONFIG_VERSION = 1;

/** Уровней глубины пять: оценщик выбирает номер, этап идёт моделью этого уровня. */
export const LEVEL_COUNT = 5;

export const ASSESSED = 'assessed';

export const SERVICE_STAGES = ['assess', 'dedupe', 'skeptic', 'threads'];

const LOGIC_MANDATE = 'Бизнес-логика: что реквест делает, делает ли он то, что просили в задаче, и что он'
  + ' ломает — доменные правила, полнота случаев, деньги, целостность данных, доступ. Файлы данных —'
  + ' локализация, .json, .yaml, .sql, спеки — разбираются по диффу и поиском: нет ли дубля, есть ли'
  + ' строка в других локалях.';
const QUALITY_MANDATE = 'Качество кода: каждый файл реквеста проходит каталог скилла reviewing-code-quality,'
  + ' и сработавший пункт становится находкой на той строке, где он сработал.';

/**
 * Встроенные этапы идут всегда. Мандат служебного этапа — в его шаблоне, у
 * разбирающих — здесь; скиллы — из `skills/` пакета.
 */
export const BUILTIN_STAGES = {
  assess: { skills: ['assessing-review-scope'], level: 1 },
  threads: { skills: [], level: 1 },
  logic: { skills: ['reviewing-business-logic'], level: ASSESSED, mandate: LOGIC_MANDATE },
  quality: { skills: ['reviewing-code-quality'], level: 2, mandate: QUALITY_MANDATE },
  dedupe: { skills: [], level: 1 },
  skeptic: { skills: ['refuting-review-findings'], level: ASSESSED },
};

// Оценщик и треды идут до вердикта: уровня от оценщика у них ещё нет.
const BEFORE_ASSESSMENT = ['assess', 'threads'];

export const DEFAULT_LEVELS = [
  { driver: 'claude', model: 'claude-sonnet-5-5', effort: 'medium' },
  { driver: 'claude', model: 'claude-sonnet-5-5', effort: 'high' },
  { driver: 'claude', model: 'claude-opus-5-5', effort: 'low' },
  { driver: 'claude', model: 'claude-opus-5-5', effort: 'medium' },
  { driver: 'claude', model: 'claude-opus-5-5', effort: 'high' },
];

const ROOT_KEYS = ['version', 'skillPaths', 'allow', 'drivers', 'levels', 'tracker', 'sharedBranches', 'stages'];
const BUILTIN_KEYS = ['skills', 'level', 'allow'];
const STAGE_KEYS = ['mandate', 'skills', 'level', 'paths', 'allow'];
const SKILLS_KEYS = ['always', 'onDemand'];
const DRIVER_KEYS = ['bin', 'env'];
const LEVEL_KEYS = ['driver', 'model', 'effort'];
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
  };
}

function parseLevels(read, raw) {
  if (raw === undefined) return DEFAULT_LEVELS;
  if (!Array.isArray(raw) || raw.length !== LEVEL_COUNT) {
    read.fail('levels', `должен быть списком из ${LEVEL_COUNT} уровней`);
  }
  return raw.map((level, index) => {
    const path = `levels[${index}]`;
    read.object(level, path, LEVEL_KEYS);
    return {
      driver: read.oneOf(level.driver, `${path}.driver`, Object.keys(DRIVER_DEFAULTS)),
      model: read.string(level.model, `${path}.model`),
      effort: read.string(level.effort, `${path}.effort`),
    };
  });
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
 * Этап конфига поверх встроенного: встроенному добавляются скиллы, задаются
 * уровень и `allow`; свой этап несёт мандат и маски путей.
 */
function parseStage(read, key, raw) {
  const path = `stages.${key}`;
  const builtin = BUILTIN_STAGES[key];
  const stage = read.object(raw, path, builtin ? BUILTIN_KEYS : STAGE_KEYS);
  if (!builtin && !STAGE_ID.test(key)) read.fail(path, `имя этапа не подходит под ${STAGE_ID}`);
  const level = parseLevel(read, stage.level, `${path}.level`, builtin?.level ?? ASSESSED);
  if (level === ASSESSED && BEFORE_ASSESSMENT.includes(key)) {
    read.fail(`${path}.level`, `у ${key} — только число от 1 до ${LEVEL_COUNT}`);
  }
  return {
    key,
    mandate: builtin ? builtin.mandate ?? null : read.string(stage.mandate, `${path}.mandate`),
    skills: parseSkills(read, stage.skills ?? {}, `${path}.skills`, builtin?.skills ?? []),
    paths: read.strings(stage.paths, `${path}.paths`),
    level,
    allow: read.strings(stage.allow, `${path}.allow`, COMMAND),
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

  const own = config.stages === undefined ? {} : read.object(config.stages, 'stages', Object.keys(Object(config.stages)));
  const stages = {};
  for (const key of new Set([...Object.keys(BUILTIN_STAGES), ...Object.keys(own)])) {
    stages[key] = parseStage(read, key, Object.hasOwn(own, key) ? own[key] : {});
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
    levels: parseLevels(read, config.levels),
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

/** Драйверы уровней: этап идёт драйвером своего уровня, а уровень от оценщика известен только в прогоне. */
export function driversInUse(config) {
  return [...new Set(config.levels.map((level) => level.driver))];
}

/** Скиллы всех этапов конфига, обоих списков. */
export function skillsInUse(config) {
  return [...new Set(Object.values(config.stages).flatMap((stage) => [...stage.skills.always, ...stage.skills.onDemand]))];
}
