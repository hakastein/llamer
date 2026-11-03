import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { readRunState } from '../src/domain/journal.mjs';
import { tempDir, write } from './fixtures/repo.mjs';

const HARNESS = new URL('../src/harness/main.mjs', import.meta.url).pathname;

const DIFF = [
  'diff --git a/src/A.php b/src/A.php',
  '--- a/src/A.php',
  '+++ b/src/A.php',
  '@@ -2,1 +2,2 @@',
  '-old',
  '+new',
  '+newer',
  '',
].join('\n');

function stage(commands) {
  const dir = tempDir('llamer-harness-');
  const journalDir = join(dir, 'journal');
  mkdirSync(journalDir);
  write(dir, 'src/A.php', 'one\nnew\nnewer\n');
  writeFileSync(join(dir, 'task.diff'), DIFF);
  writeFileSync(join(dir, 'brief.json'), JSON.stringify({
    stage: 'logic', name: 'logic', journalDir, baseSha: 'a'.repeat(40), diffPath: join(dir, 'task.diff'), commands,
  }));
  const call = (...args) => spawnSync(process.execPath, [HARNESS, ...args], {
    cwd: dir, encoding: 'utf-8', env: { ...process.env, LLAMER_BRIEF: join(dir, 'brief.json') },
  });
  return { call, state: () => readRunState(journalDir) };
}

const finding = (line) => ['finding', '--file', 'src/A.php', '--line', line, '--line-end', line,
  '--text', 'текст', '--severity', 'major', '--severity-reason', 'вес', '--related', `src/A.php:${line}`];

test('находка на изменённой строке записывается, на нетронутой — отбивается с перечнем строк', () => {
  const { call, state } = stage(['finding', 'list', 'stop']);

  assert.equal(call(...finding('3')).status, 0);
  const refused = call(...finding('1'));

  assert.equal(refused.status, 1);
  assert.match(refused.stderr, /отказ: строка 1 в src\/A\.php не изменена.*Изменённые строки: 2, 3/);
  assert.deepEqual(state().findings.map((item) => item.line), [3]);
  assert.equal(state().findings[0].stage, 'logic');
});

test('amend --suggestion проходит ту же проверку правки, что и finding', () => {
  const { call, state } = stage(['finding', 'amend', 'stop']);
  assert.equal(call(...finding('3')).status, 0);

  const same = call('amend', '--id', '1', '--suggestion', 'newer');
  assert.equal(same.status, 1);
  assert.match(same.stderr, /правка совпадает с текущим кодом/);
  assert.equal(call('amend', '--id', '1', '--suggestion', 'newest').status, 0);
  assert.equal(state().findings[0].suggestion, 'newest');
});

test('команда вне списка этапа отбивается — и одиночным вызовом, и внутри batch', () => {
  const { call, state } = stage(['finding', 'batch', 'stop']);

  const single = call('verdict', '--id', '1', '--refuted', 'true', '--reason', 'x');
  assert.equal(single.status, 1);
  assert.match(single.stderr, /команды «verdict» у этапа «logic» нет/);

  const batch = call('batch', '--json', JSON.stringify([
    { type: 'verdict', id: 1, refuted: true, reason: 'x' },
    {
      type: 'finding', file: 'src/A.php', line: 2, lineEnd: 2, text: 't', severity: 'minor', severityReason: 'r', related: 'src/A.php:2',
    },
  ]));
  assert.equal(batch.status, 0, batch.stderr);
  assert.match(batch.stdout, /элемент 1: типа «verdict» среди команд этапа нет/);
  assert.equal(state().findings.length, 1);
  assert.equal(state().findings[0].verdicted, false);
});

test('неизвестный флаг и пропущенный обязательный — отказ с подсказкой, справка — по команде', () => {
  const { call } = stage(['finding', 'stop']);

  assert.match(call('stop', '--why', 'x').stderr, /отказ: неизвестный флаг/);
  assert.match(call('stop').stderr, /отказ: нужен --reason: почему разбор прекращён/);
  const help = call('finding', '--help');
  assert.equal(help.status, 0);
  assert.match(help.stdout, /--line-end <number>.*\[обязателен\]/);
  assert.match(call('--help').stdout, /Команды этапа:\n {2}finding/);
});

test('батч приводит JSON-значения к виду флагов: false у --deleted не делает находку удалённой', () => {
  const { call, state } = stage(['batch', 'finding']);

  const done = call('batch', '--json', JSON.stringify([{
    type: 'finding', file: 'src/A.php', line: 2, lineEnd: 3, deleted: false, text: 't', severity: 'minor', severityReason: 'r', related: 'src/A.php',
  }]));

  assert.equal(done.status, 0, done.stderr);
  assert.equal(state().findings[0].deleted, false);
});
