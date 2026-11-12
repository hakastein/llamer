import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';
import {
  ConfigError, analysisStages, driversInUse, loadConfig, parseConfig, skillsInUse,
} from '../src/config.mjs';
import { CLAUDE_DEFAULTS, ClaudeDriver } from '../src/drivers/claude.mjs';
import { CodexDriver } from '../src/drivers/codex.mjs';
import { formatStages } from '../src/stages/prompt.mjs';
import { BUILTIN_SKILLS, SkillLibrary } from '../src/stages/SkillLibrary.mjs';
import {
  BUILTIN, levels, stages, tempDir, write,
} from './fixtures/repo.mjs';

const FILE = '/repo/.llamer.json';
// Встроенные этапы на claude, `stages` — поверх них.
const minimal = (extra = {}) => ({ version: 2, driver: 'claude', ...extra, stages: stages(extra.stages) });
const parse = (raw, env = {}) => parseConfig(raw, { file: FILE, env });

test('умолчания: встроенные этапы со своими скиллами и уровнями, лестницы claude и codex', () => {
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
  assert.deepEqual(config.drivers.claude.levels, CLAUDE_DEFAULTS.levels);
  assert.deepEqual(config.drivers.claude.levels.map((level) => `${level.model}/${level.effort}`), [
    'claude-sonnet-5-5/medium', 'claude-sonnet-5-5/high', 'claude-opus-5-5/low', 'claude-opus-5-5/medium', 'claude-opus-5-5/high',
  ]);
  assert.deepEqual(config.drivers.codex.levels.map((level) => `${level.model}/${level.effort}`), [
    'gpt-6.1-sol/medium', 'gpt-6.1-sol/high', 'gpt-6.1-sol/xhigh', 'gpt-6.1-sol/max', 'gpt-6.1-sol/ultra',
  ]);
  assert.deepEqual(driversInUse(config), ['claude']);
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
    drivers: ['claude'],
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

test('лестница у каждого драйвера своя: drivers.<имя>.levels заменяет умолчание только этого драйвера', () => {
  const config = parse(minimal({ drivers: { codex: { levels: levels('gpt') } } }));

  assert.deepEqual(config.drivers.codex.levels, levels('gpt'));
  assert.deepEqual(config.drivers.claude.levels, CLAUDE_DEFAULTS.levels);
});

test('драйверы этапа — явный список; драйверы прогона — драйверы всех этапов', () => {
  const config = parse(minimal({ stages: stages({}, { logic: ['claude', 'codex'] }) }));

  assert.deepEqual(config.stages.logic.drivers, ['claude', 'codex']);
  assert.deepEqual(config.stages.quality.drivers, ['claude']);
  assert.deepEqual(driversInUse(config), ['claude', 'codex']);
  assert.deepEqual(driversInUse(parse(minimal({ stages: stages({}, { all: ['codex'] }) }))), ['codex']);
  // Служебные этапы видят каждый экземпляр этапа на нескольких драйверах, одиночный — как прежде.
  assert.equal(formatStages(analysisStages(config)),
    'the business-logic reviewer (logic·claude), the business-logic reviewer (logic·codex), the code-quality reviewer,'
    + ' the comments reviewer and the test reviewer');
});

test('driver — драйвер всех этапов, drivers этапа заменяет его только у этого этапа', () => {
  const config = parse({ version: 2, driver: 'codex', stages: { logic: { drivers: ['claude', 'codex'] }, docs: { task: 'Docs.' } } });

  const drivers = Object.fromEntries(Object.values(config.stages).map((stage) => [stage.key, stage.drivers.join('+')]));
  assert.deepEqual(drivers, {
    assess: 'codex', threads: 'codex', logic: 'claude+codex', quality: 'codex', comments: 'codex', tests: 'codex', dedupe: 'codex', skeptic: 'codex', docs: 'codex',
  });
  assert.throws(() => parse({ version: 2, stages: {} }), /driver: одно из claude, codex/);
  for (const key of BUILTIN) assert.throws(() => parse(minimal({ stages: { [key]: { drivers: [] } } })), /drivers: непустой список/, key);
});

test('служебный этап на двух драйверах — ошибка; разбирающий на двух — нет', () => {
  for (const key of ['assess', 'threads', 'dedupe', 'skeptic']) {
    assert.throws(() => parse(minimal({ stages: stages({}, { [key]: ['claude', 'codex'] }) })),
      new RegExp(`stages\\.${key}\\.drivers: ${key} — служебный этап, он идёт одним экземпляром: ровно один драйвер`), key);
  }
  assert.deepEqual(parse(minimal({ stages: stages({}, { tests: ['codex', 'claude'] }) })).stages.tests.drivers, ['codex', 'claude']);
});

test('незнакомое в конфиге останавливает разбор с путём до ключа', () => {
  const cases = [
    [{ ...minimal(), version: 1 }, /version: поддерживается 2/],
    [minimal({ extra: true }), /неизвестные ключи: extra/],
    [minimal({ service: {} }), /неизвестные ключи: service/],
    [{ ...minimal(), stages: [] }, /stages: должен быть объектом/],
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
    [minimal({ levels: levels() }), /неизвестные ключи: levels/],
    [minimal({ drivers: { claude: { levels: levels().slice(1) } } }), /drivers\.claude\.levels: должен быть списком из 5/],
    [minimal({ drivers: { codex: { levels: [...levels().slice(1), { driver: 'codex', model: 'm', effort: 'e' }] } } }), /drivers\.codex\.levels\[4\]: неизвестные ключи: driver/],
    [minimal({ drivers: { codex: { levels: [...levels().slice(1), { model: 'm' }] } } }), /drivers\.codex\.levels\[4\]\.effort: должен быть непустой строкой/],
    [minimal({ stages: { logic: { drivers: 'claude' } } }), /stages\.logic\.drivers: непустой список/],
    [minimal({ stages: { logic: { drivers: ['gemini'] } } }), /stages\.logic\.drivers\[0\]: одно из claude, codex/],
    [minimal({ stages: { docs: { task: 'x', drivers: ['codex', 'claude', 'codex'] } } }), /stages\.docs\.drivers: драйвер повторяется: codex/],
    [minimal({ allow: ['rg; rm -rf /'] }), /allow\[0\]: «rg; rm -rf \/» не подходит/],
    [minimal({ stages: { skeptic: { allow: ['rg | sh'] } } }), /stages\.skeptic\.allow\[0\]: «rg \| sh» не подходит/],
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

test('drivers.<имя>.env раскрывают переменные окружения; значение с незаданной не передаётся', () => {
  const config = parse(minimal({ drivers: { claude: { env: { CLAUDE_HOME_HINT: '${TOOLS}/claude', PLAIN: 'x' } } } }), { TOOLS: '/opt/tools' });

  assert.equal(config.drivers.claude.env.CLAUDE_HOME_HINT, '/opt/tools/claude');
  assert.equal(config.drivers.claude.env.PLAIN, 'x');
  const partial = parse(minimal({ drivers: { codex: { env: { A: '${NOPE}', B: '${TOOLS}:${NOPE}', C: '${TOOLS}' } } } }), { TOOLS: '/opt/tools' });
  assert.equal(partial.drivers.codex.env.C, '/opt/tools');
  assert.ok(!('A' in partial.drivers.codex.env), 'A с незаданной NOPE не передаётся');
  assert.ok(!('B' in partial.drivers.codex.env), 'B с одной незаданной из двух не передаётся');
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

test('.llamer.local.json рядом с конфигом накладывается по RFC 7396: объекты сливаются, массивы заменяются, null удаляет', () => {
  const root = tempDir('llamer-local-');
  const file = join(root, 'ci', 'review.json');
  write(root, 'ci/review.json', JSON.stringify(minimal({
    allow: ['rg', 'git log'],
    drivers: { codex: { bin: 'codex-ci', levels: levels('gpt') } },
    stages: { logic: { drivers: ['codex'], level: 4 } },
  })));
  write(root, 'ci/.llamer.local.json', JSON.stringify({
    allow: ['rg'],
    drivers: { codex: { bin: 'codex-mine' } },
    stages: { logic: { drivers: null } },
  }));

  const config = loadConfig(file, {});

  assert.equal(config.drivers.codex.bin, 'codex-mine');
  assert.deepEqual(config.drivers.codex.levels, levels('gpt'), 'соседние ключи объекта остались');
  assert.deepEqual(config.allow, ['rg'], 'массив заменён целиком');
  assert.deepEqual(config.stages.logic.drivers, ['claude'], 'null снял переопределение — этап на корневом driver');
  assert.equal(config.stages.logic.level, 4);
  assert.equal(config.source, `${file} + ${join(root, 'ci', '.llamer.local.json')}`);
  write(root, 'ci/.llamer.local.json', JSON.stringify({ driver: 'gemini' }));
  assert.throws(() => loadConfig(file, {}), /review\.json \+ .*\.llamer\.local\.json: driver: одно из claude, codex/, 'проверяется итог наложения');
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
