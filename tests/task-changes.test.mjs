import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { TaskChangesError, taskChanges } from '../src/materials/taskChanges.mjs';
import { Diff } from '../src/materials/Diff.mjs';
import { git, tempDir, write } from './fixtures/repo.mjs';

// Ветки как в проекте с двумя общими ветками: release и develop ответвлены от
// main, ветка задачи ответвляется от одной и вливается в другую. Исключаются
// целевая ветка и общие — как их передаёт вызывающий: --exclude origin/<ветка>.
const SHARED = ['release', 'main'];

function commit(cwd, files, message) {
  for (const [path, content] of Object.entries(files)) write(cwd, path, content);
  git(cwd, 'add', '-A');
  git(cwd, 'commit', '-q', '-m', message);
  return git(cwd, 'rev-parse', 'HEAD');
}

function upstream() {
  const dir = tempDir('llamer-scope-');
  git(dir, 'init', '-q', '-b', 'main');
  git(dir, 'config', 'user.name', 'test');
  git(dir, 'config', 'user.email', 'test@localhost');
  git(dir, 'config', 'uploadpack.allowFilter', 'true');
  commit(dir, { 'shared.php': 'один\nдва\nтри\n', 'core.php': 'ядро\n' }, 'начало');
  git(dir, 'branch', 'release');
  git(dir, 'branch', 'develop');
  git(dir, 'checkout', '-q', 'release');
  commit(dir, { 'release-only.php': 'релиз\n' }, 'в релизе');
  git(dir, 'checkout', '-q', 'develop');
  commit(dir, { 'develop-only.php': 'разработка\n' }, 'в develop');
  git(dir, 'checkout', '-q', 'main');
  commit(dir, { 'main-only.php': 'main\n' }, 'в main');
  return dir;
}

const branchFrom = (dir, name, from) => git(dir, 'checkout', '-q', '-b', name, from);
const merge = (dir, branch) => git(dir, 'merge', '-q', '--no-ff', '--no-edit', branch);
const sorted = (files) => [...files].sort();

/** Клон, как в CI: ветки — в origin, дерево — на голове задачи; историю догрузил вызывающий. */
function scope(dir, {
  head, target, shallow = false, sinceMarks = null,
}) {
  const path = tempDir('llamer-scope-ws-');
  execFileSync('git', shallow
    ? ['clone', '-q', '--depth', '1', '--no-single-branch', `file://${dir}`, path]
    : ['clone', '-q', dir, path], { stdio: 'ignore' });
  git(path, 'checkout', '-q', '--detach', head);
  const requestBase = git(dir, 'merge-base', target, head);
  return taskChanges({
    cwd: path,
    head,
    excludes: [target, ...SHARED].map((branch) => `origin/${branch}`),
    requestDiff: Diff.overRange({ baseRef: requestBase, revs: [requestBase, head], cwd: dir }),
    requestBase,
    sinceMarks,
  });
}

test('ветка от release в release: разбирается ровно дифф реквеста', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const head = commit(dir, { 'task.php': 'задача\n' }, 'задача');

  const task = scope(dir, { head, target: 'release' });

  assert.deepEqual(task.diff.files, ['task.php']);
  assert.deepEqual(task.excluded, []);
  assert.equal(task.kind, 'task');
  assert.deepEqual(task.bases.map(({ rev }) => rev), ['origin/release', 'origin/main']);
});

test('ветка от release в develop: изменения release отсеяны', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const head = commit(dir, { 'task.php': 'задача\n' }, 'задача');

  const task = scope(dir, { head, target: 'develop' });

  assert.deepEqual(task.diff.files, ['task.php']);
  assert.deepEqual(task.excluded, ['release-only.php']);
});

test('влитый в ветку release отсеян, а разрешение конфликта осталось своим', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  commit(dir, { 'task.php': 'задача\n', 'shared.php': 'один\nдва задачи\nтри\n' }, 'задача');
  git(dir, 'checkout', '-q', 'release');
  commit(dir, { 'release-two.php': 'релиз 2\n', 'shared.php': 'один\nдва релиза\nтри\n' }, 'ещё в релизе');
  git(dir, 'checkout', '-q', 'PROJ-10');
  try { merge(dir, 'release'); } catch { /* конфликт в shared.php — разрешается ниже */ }
  const head = commit(dir, { 'shared.php': 'один\nдва задачи и релиза\nтри\n' }, 'Merge release into PROJ-10');

  const task = scope(dir, { head, target: 'develop' });

  assert.deepEqual(sorted(task.diff.files), ['shared.php', 'task.php']);
  assert.deepEqual(sorted(task.excluded), ['release-only.php', 'release-two.php']);
  assert.deepEqual([...task.diff.added.get('shared.php').values()].map(({ text }) => text), ['два задачи и релиза']);
});

test('перекрёстное слияние: подзадача, уже влитая в целевую ветку, отсеяна', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-1', 'release');
  branchFrom(dir, 'PROJ-2', 'release');
  commit(dir, { 'sub.php': 'подзадача\n' }, 'подзадача');
  git(dir, 'checkout', '-q', 'PROJ-1');
  merge(dir, 'PROJ-2');
  branchFrom(dir, 'PROJ-3', 'PROJ-1');
  commit(dir, { 'task.php': 'задача\n' }, 'задача');
  git(dir, 'checkout', '-q', 'release');
  commit(dir, { 'release-two.php': 'релиз 2\n' }, 'ещё в релизе');
  git(dir, 'checkout', '-q', 'PROJ-3');
  merge(dir, 'release');
  const head = git(dir, 'rev-parse', 'HEAD');
  git(dir, 'checkout', '-q', 'PROJ-1');
  merge(dir, 'release');

  const task = scope(dir, { head, target: 'PROJ-1' });

  assert.deepEqual(task.diff.files, ['task.php']);
});

test('develop, влитый в ветку для release, остаётся в разборе: в release его нет', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  commit(dir, { 'task.php': 'задача\n' }, 'задача');
  merge(dir, 'develop');
  const head = git(dir, 'rev-parse', 'HEAD');

  const task = scope(dir, { head, target: 'release' });

  assert.deepEqual(sorted(task.diff.files), ['develop-only.php', 'task.php']);
});

test('конфликт между общими ветками: конфликтный файл разбирается по диффу реквеста', () => {
  const dir = upstream();
  git(dir, 'checkout', '-q', 'main');
  commit(dir, { 'shared.php': 'один\nдва из main\nтри\n' }, 'в main');
  git(dir, 'checkout', '-q', 'release');
  commit(dir, { 'shared.php': 'один\nдва релиза\nтри\n' }, 'в релизе');
  branchFrom(dir, 'PROJ-10', 'release');
  commit(dir, { 'task.php': 'задача\n' }, 'задача');
  try { merge(dir, 'main'); } catch { /* конфликт в shared.php — разрешается ниже */ }
  const head = commit(dir, { 'shared.php': 'один\nдва из main и релиза\nтри\n' }, 'Merge main into PROJ-10');

  const task = scope(dir, { head, target: 'release' });

  assert.equal(task.kind, 'mixed');
  assert.deepEqual(task.conflicted, ['shared.php']);
  assert.deepEqual(sorted(task.diff.files), ['shared.php', 'task.php']);
  assert.deepEqual(task.excluded, ['main-only.php']);
  assert.deepEqual([...task.diff.removed.get('shared.php').values()].map(({ text }) => text), ['два релиза']);
});

test('реквест без своих изменений: разбирать нечего', () => {
  const dir = upstream();
  const head = git(dir, 'rev-parse', 'release');

  const task = scope(dir, { head, target: 'develop' });

  assert.equal(task.diff.isEmpty, true);
  assert.deepEqual(task.excluded, ['release-only.php']);
});

test('неглубокий клон без общей истории — ошибка, называющая причину', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const head = commit(dir, { 'task.php': 'задача\n' }, 'задача');

  assert.throws(() => scope(dir, { head, target: 'develop', shallow: true }),
    (error) => error instanceof TaskChangesError && /нет общей истории — клон неглубокий/.test(error.message));
});

test('--since-last: от метки, а влитое после неё из общей ветки отсеяно', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const mark = commit(dir, { 'task.php': 'задача\n' }, 'задача');
  git(dir, 'checkout', '-q', 'release');
  commit(dir, { 'release-late.php': 'поздний релиз\n' }, 'релиз ушёл вперёд');
  git(dir, 'checkout', '-q', 'PROJ-10');
  merge(dir, 'release');
  const head = commit(dir, { 'task-2.php': 'правка после прогона\n' }, 'после прогона');

  const task = scope(dir, { head, target: 'develop', sinceMarks: [mark] });

  assert.deepEqual(task.diff.files, ['task-2.php']);
  assert.equal(task.since, mark);
  assert.deepEqual(task.bases.map(({ rev }) => rev), ['origin/develop', 'origin/release', 'origin/main', 'прошлый прогон']);
});

test('--since-last: две метки в истории — счёт от поздней, в каком бы порядке они ни пришли', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const early = commit(dir, { 'task.php': 'задача\n' }, 'первый прогон');
  const late = commit(dir, { 'task-2.php': 'правка\n' }, 'второй прогон');
  const head = commit(dir, { 'task-3.php': 'после прогонов\n' }, 'после прогонов');

  for (const sinceMarks of [[late, early], [early, late]]) {
    const task = scope(dir, { head, target: 'develop', sinceMarks });

    assert.equal(task.since, late);
    assert.deepEqual(task.diff.files, ['task-3.php']);
  }
});

test('--since-last: метка совпала с базой реквеста — разбирается весь реквест', () => {
  const dir = upstream();
  branchFrom(dir, 'PROJ-10', 'release');
  const head = commit(dir, { 'task.php': 'задача\n' }, 'задача');
  const requestBase = git(dir, 'merge-base', 'develop', head);

  const task = scope(dir, { head, target: 'develop', sinceMarks: [requestBase] });

  assert.equal(task.since, null);
  assert.deepEqual(task.diff.files, ['task.php']);
  assert.deepEqual(task.bases.map(({ rev }) => rev), ['origin/develop', 'origin/release', 'origin/main']);
});
