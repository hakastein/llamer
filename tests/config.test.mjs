import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { parseConfig, ConfigError } from '../src/config.mjs';
import { SkillLibrary } from '../src/stages/SkillLibrary.mjs';
import { tempDir, write } from './fixtures/repo.mjs';

const FILE = '/repo/.llamer.json';
const minimal = (extra = {}) => ({ version: 1, stages: [{ id: 'logic', mandate: 'Логика.' }], ...extra });
const parse = (raw, env = {}) => parseConfig(raw, { file: FILE, env });

test('умолчания: claude на всех этапах, разбор на глубине оценщика, трекера нет', () => {
  const config = parse(minimal());

  assert.equal(config.stages[0].driver, 'claude');
  assert.equal(config.stages[0].level, 'assessed');
  assert.equal(config.service.skeptic.driver, 'claude');
  assert.deepEqual(config.tracker, { type: 'none', issuePattern: null });
});

test('уровни драйвера из конфига заменяют встроенные', () => {
  const levels = [1, 2, 3, 4, 5].map((n) => ({ model: `model-${n}`, effort: `effort-${n}` }));

  assert.deepEqual(parse(minimal({ drivers: { codex: { levels } } })).drivers.codex.levels, levels);
});

test('незнакомое в конфиге останавливает разбор с путём до ключа', () => {
  const cases = [
    [{ ...minimal(), version: 2 }, /version: поддерживается 1/],
    [minimal({ extra: true }), /неизвестные ключи: extra/],
    [minimal({ stages: [{ id: 'logic', mandate: 'x', model: 'opus' }] }), /stages\[0\]: неизвестные ключи: model/],
    [minimal({ service: { reviewer: {} } }), /service: неизвестные ключи: reviewer/],
    [minimal({ drivers: { gemini: {} } }), /drivers: неизвестные ключи: gemini/],
    [minimal({ stages: [{ id: 'skeptic', mandate: 'x' }] }), /«skeptic» уже занят служебным этапом/],
    [minimal({ stages: [{ id: 'a', mandate: 'x' }, { id: 'a', mandate: 'y' }] }), /«a» уже занят/],
    [minimal({ stages: [] }), /stages: нужен хотя бы один/],
    [minimal({ allow: ['rg; rm -rf /'] }), /allow\[0\]: «rg; rm -rf \/» не подходит/],
    [minimal({ tracker: { type: 'youtrack' } }), /tracker\.issuePattern: должен быть непустой строкой/],
    [minimal({ drivers: { claude: { levels: [{ model: 'm', effort: 'e' }] } } }), /levels: должен быть списком из 5/],
    [minimal({ stages: [{ id: 'logic', mandate: 'x', level: 9 }] }), /level: «assessed» или число от 1 до 5/],
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
