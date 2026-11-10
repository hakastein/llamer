import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { readRunState } from '../src/domain/journal.mjs';
import { Diff } from '../src/materials/Diff.mjs';
import { foldDeleted, widestFittingDiff } from '../src/materials/gather.mjs';
import { ContextPack } from '../src/materials/ContextPack.mjs';
import { numberDiffLines } from '../src/materials/numberedDiff.mjs';
import { git, tempDir, write } from './fixtures/repo.mjs';

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

// База и голова с файлами всех статусов; изменение в середине длинного файла — ширина контекста видна.
function diffStage() {
  const root = tempDir('llamer-diff-');
  const numbers = (from, to) => Array.from({ length: to - from + 1 }, (_, index) => `${from + index}`).join('\n').concat('\n');
  git(root, 'init', '-q');
  write(root, 'src/Kept.php', numbers(1, 30));
  write(root, 'src/Gone.php', numbers(1, 30));
  write(root, 'src/Old.php', numbers(1, 20));
  git(root, 'add', '-A');
  git(root, '-c', 'user.name=t', '-c', 'user.email=t@example.com', 'commit', '-qm', 'base');
  const base = git(root, 'rev-parse', 'HEAD');
  write(root, 'src/Kept.php', numbers(1, 30).replace('\n15\n', '\nfifteen\n'));
  git(root, 'rm', '-q', 'src/Gone.php');
  git(root, 'mv', 'src/Old.php', 'src/New.php');
  write(root, 'src/New.php', numbers(1, 21));
  write(root, 'src/Fresh.php', 'a\nb\n');
  git(root, 'add', '-A');
  git(root, '-c', 'user.name=t', '-c', 'user.email=t@example.com', 'commit', '-qm', 'head');
  const revs = [base, git(root, 'rev-parse', 'HEAD')];
  writeFileSync(join(root, '.git', 'brief.json'), JSON.stringify({
    stage: 'logic', journalDir: join(root, '.git'), baseSha: base, revs, diffPath: join(root, '.git', 'HEAD'), commands: ['diff'],
  }));
  const call = (...args) => spawnSync(process.execPath, [HARNESS, ...args], {
    cwd: root, encoding: 'utf-8', env: { ...process.env, LLAMER_BRIEF: join(root, '.git', 'brief.json') },
  });
  // Секция diff пакета для тех же ревизий, разрезанная по файлам.
  const diff = Diff.overRange({ baseRef: base, revs, cwd: root });
  const pack = widestFittingDiff(diff, Infinity);
  const section = (file, text = pack.text) => text.split(/(?=^diff --git )/m).find((chunk) => chunk.split('\n')[0].endsWith(` b/${file}`)).trimEnd();
  return {
    call, diff, pack, section,
  };
}

test('review diff: файл — как в секции diff пакета, несколько путей за вызов, путь вне изменений — строка, а не отказ', () => {
  const { call, pack, section } = diffStage();
  assert.equal(pack.context, 10);
  assert.match(section('src/Kept.php'), /\n {4}5  5\n[^]*\n {5} -15\n {3}15 \+fifteen\n[^]*\n {3}25  25$/, 'контекст пакета — десять строк');

  const one = call('diff', 'src/Kept.php');
  assert.equal(one.status, 0, one.stderr);
  assert.equal(one.stdout, `${section('src/Kept.php')}\n`);

  const several = call('diff', 'src/Gone.php', 'src/Missing.php', 'src/New.php');
  assert.equal(several.status, 0, several.stderr);
  assert.equal(several.stdout, [section('src/Gone.php'),
    'src/Missing.php: среди изменений задачи такого файла нет; перечень — review diff без аргументов',
    section('src/New.php'), ''].join('\n'));
});

test('переименование с правкой: в пакете и review diff — rename и только изменённая строка, тред — по новому пути со старым', () => {
  const { call, diff, section } = diffStage();
  const renamed = section('src/New.php');
  assert.match(renamed, /^diff --git a\/src\/Old\.php b\/src\/New\.php\nsimilarity index \d+%\nrename from src\/Old\.php\nrename to src\/New\.php\n/);
  assert.deepEqual(renamed.split('\n').filter((row) => /^ *\d* [+-]/.test(row)), ['   21 +21']);
  assert.equal(call('diff', 'src/New.php').stdout, `${renamed}\n`);
  assert.deepEqual(diff.linesOf('src/New.php'), [21]);
  assert.equal(diff.oldPathOf('src/New.php'), 'src/Old.php');
});

test('review diff без путей — изменённые файлы со статусом и числом строк; справка — одной строкой с путями', () => {
  const { call } = diffStage();

  assert.equal(call('diff').stdout, [
    'src/Fresh.php — добавлен, +2 −0',
    'src/Gone.php — удалён, +0 −30',
    'src/Kept.php — изменён, +1 −1',
    'src/New.php — переименован из src/Old.php, +1 −0',
    '',
  ].join('\n'));
  assert.match(call('diff', '--help').stdout, /^Usage: review diff \[options\] \[path\.\.\.\]\n\nthe task's diff of the given files/);
  assert.match(call('--help').stdout, /\n {2}diff {2}the task's diff of the given files/);
});

test('пакет: полный дифф влезает — удалённый файл целиком, дифф байт в байт как самый широкий', () => {
  const { diff, pack, section } = diffStage();
  const full = numberDiffLines(diff.text({ context: 10 }));

  assert.equal(pack.text, full);
  assert.equal(widestFittingDiff(diff, full.length).text, full, 'ровно в бюджет — тоже целиком');
  assert.match(section('src/Gone.php'), /\n {6}-1\n {6}-2\n[^]*\n {6}-30$/);
  assert.equal(pack.folded, 0);
  assert.doesNotMatch(new ContextPack({ diff: pack.text, folded: pack.folded }).shared(), /deleted files/);
});

test('пакет: полный дифф не влезает — удалённые свёрнуты в заглушку, остальные целиком, этапу сказано', () => {
  const { diff, section } = diffStage();
  const full = numberDiffLines(diff.text({ context: 10 }));
  const pack = widestFittingDiff(diff, full.length - 1);

  assert.equal(pack.context, 10);
  assert.equal(pack.folded, 1);
  assert.equal(pack.saved, full.length - pack.text.length);
  assert.equal(
    section('src/Gone.php', pack.text),
    section('src/Gone.php').replace(/^@@[^]*/m, '@@ -1,30 +0,0 @@ deleted file, 30 lines removed; the content: review diff src/Gone.php'),
  );
  assert.match(section('src/Gone.php', pack.text), /\ndeleted file mode 100644\n/);
  for (const file of ['src/Fresh.php', 'src/Kept.php', 'src/New.php']) assert.equal(section(file, pack.text), section(file));
  assert.deepEqual([widestFittingDiff(diff, 0).context, widestFittingDiff(diff, 0).folded], [0, 1], 'не влезает и свёрнутым — самый узкий, его режет пакет');
  const short = 'diff --git a/x b/x\ndeleted file mode 100644\n--- a/x\n+++ /dev/null\n@@ -1 +0,0 @@\n      -1\n';
  assert.deepEqual(foldDeleted(short), { text: short, folded: 0 }, 'короткий файл заглушка не удлиняет');
  assert.match(
    new ContextPack({ diff: pack.text, folded: pack.folded }).shared(),
    /<attention>\nThe content of 1 deleted files is folded to one line each — the whole diff does not fit\. Read a deleted file with `review diff <path>`\./,
  );
});

test('review diff удалённого файла при свёрнутом пакете — всё удалённое содержимое', () => {
  const { diff, call, section } = diffStage();
  const folded = widestFittingDiff(diff, numberDiffLines(diff.text({ context: 10 })).length - 1);
  assert.doesNotMatch(section('src/Gone.php', folded.text), /-1\n/);

  const gone = call('diff', 'src/Gone.php');
  assert.equal(gone.status, 0, gone.stderr);
  assert.equal(gone.stdout, `${section('src/Gone.php')}\n`);
  assert.match(gone.stdout, /\n {6}-1\n {6}-2\n[^]*\n {6}-30\n$/);
});
