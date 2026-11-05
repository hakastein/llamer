import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';
import {
  ConfigError, DEFAULT_LEVELS, analysisStages, driversInUse, parseConfig, skillsInUse,
} from '../src/config.mjs';
import { ClaudeDriver } from '../src/drivers/claude.mjs';
import { CodexDriver } from '../src/drivers/codex.mjs';
import { BUILTIN_SKILLS, SkillLibrary } from '../src/stages/SkillLibrary.mjs';
import { tempDir, write } from './fixtures/repo.mjs';

const FILE = '/repo/.llamer.json';
const minimal = (extra = {}) => ({ version: 1, ...extra });
const parse = (raw, env = {}) => parseConfig(raw, { file: FILE, env });
const levels = (driver = 'claude') => [1, 2, 3, 4, 5].map((n) => ({ driver, model: `model-${n}`, effort: `effort-${n}` }));

test('умолчания: встроенные этапы со своими скиллами и уровнями, пять уровней claude, трекера нет', () => {
  const config = parse(minimal());

  assert.deepEqual(Object.keys(config.stages), ['assess', 'threads', 'logic', 'quality', 'comments', 'tests', 'dedupe', 'skeptic']);
  const brief = Object.fromEntries(Object.values(config.stages).map((stage) => [stage.key, [stage.level, stage.skills.always, stage.paths]]));
  assert.deepEqual(brief, {
    assess: [1, ['assessing-review-scope'], []],
    threads: [1, [], []],
    logic: ['assessed', ['reviewing-business-logic'], []],
    quality: [2, ['reviewing-code-quality'], []],
    comments: [1, [], []],
    tests: [1, [], ['**/tests/**']],
    dedupe: [1, [], []],
    skeptic: ['assessed', ['refuting-review-findings'], []],
  });
  assert.deepEqual(analysisStages(config).map((stage) => stage.title), [
    'the business-logic reviewer', 'the code-quality reviewer', 'the comments reviewer', 'the test reviewer',
  ]);
  assert.ok(Object.values(config.stages).every((stage) => stage.task === null && Object.keys(stage.prompt).length === 0));
  assert.deepEqual(config.levels, DEFAULT_LEVELS);
  assert.deepEqual(driversInUse(config), ['claude']);
  assert.deepEqual(config.tracker, { type: 'none', issuePattern: null });
});

test('встроенному этапу конфиг добавляет скиллы и задаёт level, allow и маски; свой этап — после встроенных', () => {
  const config = parse(minimal({
    stages: {
      docs: { task: 'Docs.', paths: ['docs/**'], skills: { always: ['writing-docs'] } },
      logic: { skills: { always: ['project-knowledge'], onDemand: ['developing-backend'] }, level: 4, allow: ['git log'] },
      tests: { paths: ['**/tests/**', '!tests/e2e/**'] },
    },
  }));

  assert.deepEqual(config.stages.logic.skills, { always: ['reviewing-business-logic', 'project-knowledge'], onDemand: ['developing-backend'] });
  assert.equal(config.stages.logic.level, 4);
  assert.deepEqual(config.stages.logic.allow, ['git log']);
  assert.deepEqual(config.stages.tests.paths, ['**/tests/**', '!tests/e2e/**']);
  assert.deepEqual(analysisStages(config).map((stage) => stage.key), ['logic', 'quality', 'comments', 'tests', 'docs']);
  assert.deepEqual(config.stages.docs, {
    key: 'docs',
    title: 'the docs stage',
    task: 'Docs.',
    skills: { always: ['writing-docs'], onDemand: [] },
    paths: ['docs/**'],
    level: 'assessed',
    allow: [],
    prompt: {},
  });
  assert.deepEqual(skillsInUse(config).sort(), [
    'assessing-review-scope', 'developing-backend', 'project-knowledge', 'refuting-review-findings',
    'reviewing-business-logic', 'reviewing-code-quality', 'writing-docs',
  ]);
});

test('prompt: общий идёт всем этапам, prompt этапа перекрывает его по ключам', () => {
  const config = parse(minimal({
    prompt: { protocol: 'nav is up.', plan: 'Plan with nav.' },
    stages: { assess: { prompt: { plan: 'Assess plan.' } }, docs: { task: 'Docs.', prompt: { protocol: 'docs nav.' } } },
  }));

  assert.deepEqual(config.stages.quality.prompt, { protocol: 'nav is up.', plan: 'Plan with nav.' });
  assert.deepEqual(config.stages.assess.prompt, { protocol: 'nav is up.', plan: 'Assess plan.' });
  assert.deepEqual(config.stages.docs.prompt, { protocol: 'docs nav.', plan: 'Plan with nav.' });
});

test('levels из конфига заменяют встроенные; драйверы прогона — те, что в levels', () => {
  const mixed = [...levels('claude').slice(0, 4), { driver: 'codex', model: 'gpt', effort: 'high' }];
  const config = parse(minimal({ levels: mixed }));

  assert.deepEqual(config.levels, mixed);
  assert.deepEqual(driversInUse(config), ['claude', 'codex']);
  assert.deepEqual(driversInUse(parse(minimal({ levels: levels('codex') }))), ['codex']);
});

test('незнакомое в конфиге останавливает разбор с путём до ключа', () => {
  const cases = [
    [{ ...minimal(), version: 2 }, /version: поддерживается 1/],
    [minimal({ extra: true }), /неизвестные ключи: extra/],
    [minimal({ service: {} }), /неизвестные ключи: service/],
    [minimal({ stages: [] }), /stages: должен быть объектом/],
    [minimal({ stages: { logic: { task: 'x' } } }), /stages\.logic: неизвестные ключи: task/],
    [minimal({ stages: { docs: { mandate: 'x', paths: ['docs/**'] } } }), /stages\.docs: неизвестные ключи: mandate/],
    [minimal({ stages: { assess: { paths: ['src/**'] } } }), /stages\.assess: неизвестные ключи: paths/],
    [minimal({ stages: { comments: { task: 'x' } } }), /stages\.comments: неизвестные ключи: task/],
    [minimal({ prompt: { system: 'x' } }), /prompt: неизвестные ключи: system/],
    [minimal({ stages: { quality: { prompt: { plan: '' } } } }), /stages\.quality\.prompt\.plan: должен быть непустой строкой/],
    [minimal({ stages: { docs: { paths: ['docs/**'] } } }), /stages\.docs\.task: должен быть непустой строкой/],
    [minimal({ stages: { docs: { task: 'x', id: 'docs' } } }), /stages\.docs: неизвестные ключи: id/],
    [minimal({ stages: { docs: { task: 'x', driver: 'codex' } } }), /stages\.docs: неизвестные ключи: driver/],
    [minimal({ stages: { Docs: { task: 'x' } } }), /stages\.Docs: имя этапа не подходит/],
    [minimal({ stages: { docs: { task: 'x', skills: ['a'] } } }), /stages\.docs\.skills: должен быть объектом/],
    [minimal({ stages: { docs: { task: 'x', skills: { always: ['a'], onDemand: ['a'] } } } }), /stages\.docs\.skills: скилл в обоих списках, always и onDemand: a/],
    [minimal({ stages: { logic: { skills: { onDemand: ['reviewing-business-logic'] } } } }), /stages\.logic\.skills: скилл в обоих списках/],
    [minimal({ stages: { docs: { task: 'x', skills: { sometimes: [] } } } }), /stages\.docs\.skills: неизвестные ключи: sometimes/],
    [minimal({ stages: { docs: { task: 'x', level: 9 } } }), /stages\.docs\.level: «assessed» или число от 1 до 5/],
    [minimal({ stages: { assess: { level: 'assessed' } } }), /stages\.assess\.level: у assess — только число/],
    [minimal({ stages: { threads: { level: 'assessed' } } }), /stages\.threads\.level: у threads — только число/],
    [minimal({ drivers: { gemini: {} } }), /drivers: неизвестные ключи: gemini/],
    [minimal({ drivers: { claude: { levels: levels() } } }), /drivers\.claude: неизвестные ключи: levels/],
    [minimal({ levels: levels().slice(1) }), /levels: должен быть списком из 5/],
    [minimal({ levels: [...levels().slice(1), { driver: 'gemini', model: 'm', effort: 'e' }] }), /levels\[4\]\.driver: одно из claude, codex/],
    [minimal({ levels: [...levels().slice(1), { model: 'm', effort: 'e' }] }), /levels\[4\]\.driver: одно из claude, codex/],
    [minimal({ allow: ['rg; rm -rf /'] }), /allow\[0\]: «rg; rm -rf \/» не подходит/],
    [minimal({ stages: { skeptic: { allow: ['rg | sh'] } } }), /stages\.skeptic\.allow\[0\]: «rg \| sh» не подходит/],
    [minimal({ tracker: { type: 'youtrack' } }), /tracker\.issuePattern: должен быть непустой строкой/],
    [minimal({ skillPaths: ['${NOPE}/skills'] }), /переменная NOPE не задана/],
  ];
  for (const [raw, message] of cases) {
    assert.throws(() => parse(raw), (error) => error instanceof ConfigError && message.test(error.message), String(message));
  }
});

test('skillPaths раскрывают переменные окружения и считаются от каталога конфига', () => {
  const config = parse(minimal({ skillPaths: ['${TOOLS}/skills', 'local'] }), { TOOLS: '/opt/tools' });

  assert.deepEqual(config.skillPaths, ['/opt/tools/skills', '/repo/local']);
});

test('drivers.<имя>.env раскрывают переменные окружения, как skillPaths', () => {
  const config = parse(minimal({ drivers: { claude: { env: { CLAUDE_HOME_HINT: '${TOOLS}/claude', PLAIN: 'x' } } } }), { TOOLS: '/opt/tools' });

  assert.equal(config.drivers.claude.env.CLAUDE_HOME_HINT, '/opt/tools/claude');
  assert.equal(config.drivers.claude.env.PLAIN, 'x');
  assert.throws(() => parse(minimal({ drivers: { codex: { env: { A: '${NOPE}' } } } })), /drivers\.codex\.env\.A: переменная NOPE не задана/);
});

test('одноимённые скиллы в двух каталогах — ошибка', () => {
  const root = tempDir('llamer-skills-');
  write(root, 'a/same/SKILL.md', 'a');
  write(root, 'b/same/SKILL.md', 'b');
  write(root, 'b/other/SKILL.md', 'b');

  assert.throws(() => SkillLibrary.load([join(root, 'a'), join(root, 'b')]), /скилл same есть в двух каталогах/);
  assert.deepEqual(SkillLibrary.load([join(root, 'b')]).missing(['same', 'other', 'nope']), ['nope']);
});

test('одноимённый со встроенным скилл в skillPaths не конфликтует: этап получает встроенный', () => {
  const root = tempDir('llamer-skills-');
  write(root, 'own/reviewing-code-quality/SKILL.md', 'свой');

  const library = SkillLibrary.load([join(root, 'own')]);

  assert.equal(library.dirOf('reviewing-code-quality'), join(BUILTIN_SKILLS, 'reviewing-code-quality'));
});

test('задание: always — раздел «Your Method» старого движка, onDemand в задании не называется', () => {
  const root = tempDir('llamer-skills-');
  write(root, 'own/method/SKILL.md', '---\nname: method\n---\nМетод.\n');
  write(root, 'own/backend/SKILL.md', '---\nname: backend\ndescription: "Use when PHP changes"\n---\nБэкенд.\n');
  const library = SkillLibrary.load([join(root, 'own')]);
  const skills = { always: ['method'], onDemand: ['backend'] };
  const method = (how, ref) => `## Your Method\n\n${how} They are your method,\n`
    + `they are not optional, and nothing below repeats what is in them.\n\n- ${ref}`;

  assert.equal(new ClaudeDriver({ settings: {}, env: {}, runSkills: '/run' }).skillsBlock(library, skills),
    method('Load each of these with the Skill tool before you start.', 'stage:method'));
  assert.equal(new CodexDriver({ settings: {}, env: {}, runSkills: '/run' }).skillsBlock(library, skills),
    method('Each of these is attached by its mention below — its text is already with you, follow it from the start.', '$method'));
  assert.equal(new ClaudeDriver({ settings: {}, env: {} }).skillsBlock(library, { always: [], onDemand: ['backend'] }), '');
  assert.throws(() => new ClaudeDriver({ settings: {}, env: {} }).skillsBlock(library, { always: [], onDemand: ['nope'] }), /скиллы не найдены: nope/);
});
