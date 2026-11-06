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

// Несколько этапов одного прогона — общий `journalDir`, у каждого свой файл журнала.
function stage(commands, {
  name = 'logic', journalDir = null, threads = [], relatedRequired = true, siblings = [],
} = {}) {
  const dir = tempDir('llamer-harness-');
  const journal = journalDir ?? join(dir, 'journal');
  mkdirSync(journal, { recursive: true });
  write(dir, 'src/A.php', 'one\nnew\nnewer\n');
  writeFileSync(join(dir, 'task.diff'), DIFF);
  writeFileSync(join(dir, 'brief.json'), JSON.stringify({
    stage: name, name, journalDir: journal, baseSha: 'a'.repeat(40), diffPath: join(dir, 'task.diff'), commands, threads, relatedRequired, siblings,
  }));
  const call = (...args) => spawnSync(process.execPath, [HARNESS, ...args], {
    cwd: dir, encoding: 'utf-8', env: { ...process.env, LLAMER_BRIEF: join(dir, 'brief.json') },
  });
  return { call, journalDir: journal, state: () => readRunState(journal) };
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
  assert.match(call('stop').stderr, /отказ: нужен --reason: why the review was aborted/);
  const help = call('finding', '--help');
  assert.equal(help.status, 0);
  // Раскладка commander: колонка флагов по самому длинному, описание переносится в 80 символов.
  assert.match(help.stdout, /^Usage: review finding \[options\]\n\nrecord a remark on the request, anchored to diff lines\n/);
  assert.match(help.stdout, /\n {2}--line-end <number> {7}last line of it; no default, so repeat --line for a\n {28}one-line remark \[required\]\n/);
  assert.match(help.stdout, /\(choices: "minor", "major", "critical"\)/);
  assert.match(help.stdout, /\n {2}-h, --help {16}display this message\n/);
  assert.match(call('--help').stdout, /Stage commands:\n {2}finding/);
});

test('находке комментариев --related не обязателен: место доказывает сам комментарий', () => {
  const bare = finding('3').slice(0, -2);

  assert.match(stage(['finding']).call(...bare).stderr, /нужен --related/);
  assert.equal(stage(['finding'], { relatedRequired: false }).call(...bare).status, 0);
});

test('батч приводит JSON-значения к виду флагов: false у --deleted не делает находку удалённой', () => {
  const { call, state } = stage(['batch', 'finding']);

  const done = call('batch', '--json', JSON.stringify([{
    type: 'finding', file: 'src/A.php', line: 2, lineEnd: 3, deleted: false, text: 't', severity: 'minor', severityReason: 'r', related: 'src/A.php',
  }]));

  assert.equal(done.status, 0, done.stderr);
  assert.equal(state().findings[0].deleted, false);
});

test('часы шагнули назад между этапами — dupe всё равно ложится после своих находок', () => {
  // Находки записаны, пока часы спешили на минуту: их `at` — в будущем.
  const { call, journalDir, state } = stage(['dupe'], { name: 'dedupe' });
  const found = JSON.stringify({
    type: 'finding', file: 'src/A.php', line: 2, lineEnd: 2, text: 't', severity: 'major', at: Date.now() + 60_000, stage: 'logic',
  });
  writeFileSync(join(journalDir, 'logic.jsonl'), `${found}\n${found}\n`);

  const done = call('dupe', '--id', '2', '--of', '1', '--reason', 'то же место');

  assert.equal(done.status, 0, done.stderr);
  assert.equal(state().findings[1].duplicateOf, 1);
});

test('закрытый тред не ломает порядок свёртки: dupe этапа, чей файл раньше threads, не теряется', () => {
  // Файлы журнала читаются по имени: service.dedupe, service.threads, tests.
  const tests = stage(['finding'], { name: 'tests' });
  assert.equal(tests.call(...finding('2')).status, 0);
  assert.equal(tests.call(...finding('3')).status, 0);
  const threads = stage(['resolve'], {
    name: 'service.threads', journalDir: tests.journalDir, threads: [{ id: 't1', at: 'src/Order.php:4', text: 'замечание' }],
  });
  assert.equal(threads.call('resolve', '--id', 't1', '--reason', 'снято').status, 0);
  const dedupe = stage(['dupe'], { name: 'service.dedupe', journalDir: tests.journalDir });
  assert.equal(dedupe.call('dupe', '--id', '2', '--of', '1', '--reason', 'то же').status, 0);

  const state = tests.state();
  assert.equal(state.findings[1].duplicateOf, 1);
  assert.deepEqual(state.resolved, [{ id: 't1', reason: 'снято', place: 'src/Order.php:4' }]);
});

test('экземпляр этапа не видит находок другого экземпляра этого же этапа; другой этап видит все', () => {
  const claude = stage(['finding', 'list'], { name: 'logic·claude', siblings: ['logic·codex'] });
  assert.equal(claude.call(...finding('3')).status, 0);
  const codex = stage(['finding', 'amend', 'list'], { name: 'logic·codex', journalDir: claude.journalDir, siblings: ['logic·claude'] });

  assert.equal(codex.call('list').stdout.trim(), 'находок пока нет');
  assert.match(codex.call('amend', '--id', '1', '--text', 'чужая').stderr, /находки #1 в журнале нет/);
  assert.equal(codex.call(...finding('3')).status, 0);
  assert.match(codex.call('list').stdout, /^2\. \[major\] src\/A\.php:3/, 'номер сквозной по прогону');
  assert.equal(codex.call('amend', '--id', '2', '--text', 'своя').status, 0);

  const quality = stage(['list'], { name: 'quality', journalDir: claude.journalDir });
  assert.equal(quality.call('list').stdout.trim().split('\n').length, 2);
  assert.deepEqual(claude.state().findings.map((item) => [item.stage, item.text]), [['logic·claude', 'текст'], ['logic·codex', 'своя']]);
});
