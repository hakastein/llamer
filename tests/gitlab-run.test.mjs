import { test } from 'node:test';
import assert from 'node:assert/strict';
import { git, makeRepo, run, SECRETS } from './fixtures/repo.mjs';
import { startGitlab } from './fixtures/gitlab.mjs';

const PROJECT = 'group/demo';
const IID = '7';

// GitLab отдаёт дифф файла без заголовков — с первого ханка.
function changesOf(root, base, head) {
  return git(root, 'diff', '--name-only', base, head).split('\n').map((path) => {
    const diff = git(root, 'diff', base, head, '--', path);
    return {
      new_path: path,
      old_path: path,
      new_file: /^new file mode/m.test(diff),
      deleted_file: false,
      renamed_file: false,
      diff: `${diff.slice(diff.indexOf('@@'))}\n`,
    };
  });
}

// Реквест из настоящего репозитория: дерево стоит на голове ветки, как клон CI.
// Наш открытый тред прошлого прогона висит на src/Order.php — его снимет этап тредов.
async function pipeline() {
  const root = makeRepo();
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'finish');
  const base = git(root, 'merge-base', 'main', 'HEAD');
  const head = git(root, 'rev-parse', 'HEAD');
  const gitlab = await startGitlab({
    project: PROJECT,
    iid: IID,
    token: SECRETS.GITLAB_TOKEN,
    mr: {
      title: 'Заказы',
      source_branch: 'feature/dev-7-orders',
      target_branch: 'main',
      diff_refs: { base_sha: base, head_sha: head, start_sha: base },
    },
    changes: changesOf(root, base, head),
    versions: [{ head_commit_sha: head, base_commit_sha: base, start_commit_sha: base }],
    discussions: [{
      id: 'old-thread',
      notes: [{
        body: `Верни 3.\n\n<sub>ревизия</sub><!-- llamer-rev:${base} -->`,
        author: { username: 'bot' },
        position: { position_type: 'text', new_path: 'src/Order.php', new_line: 4 },
      }],
    }],
  });
  return {
    root, base, head, gitlab, mr: `${gitlab.url}/${PROJECT}/-/merge_requests/${IID}`,
  };
}

test('--mr без публикации: отчёт по MR, в GitLab только чтение', async () => {
  const { root, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr]);

    assert.equal(result.status, 0, result.stderr);
    const answer = JSON.parse(result.stdout);
    assert.equal(answer.input, 'gitlab');
    assert.equal(answer.findings.length, 1);
    assert.deepEqual(answer.resolvedThreads.map((thread) => thread.id), ['old-thread']);
    assert.ok(result.launches.some((launch) => launch.stage === 'threads'), 'этап тредов поднят');
    assert.deepEqual([...new Set(gitlab.requests.map((request) => request.method))], ['GET']);
    assert.ok(gitlab.requests.every((request) => request.token === SECRETS.GITLAB_TOKEN));
  } finally {
    await gitlab.close();
  }
});

test('--mr --publish gitlab: заметка о рисках, закрытый тред, тред на строке, сводка и статус', async () => {
  const { root, head, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr, '--publish', 'gitlab']);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, '', 'при публикации stdout пуст');
    const writes = gitlab.requests.filter((request) => request.method !== 'GET');
    const notes = writes.filter((request) => request.path.endsWith('/notes')).map((request) => request.body.body);
    assert.match(notes[0], /<!-- llamer-assessment -->/);
    assert.match(notes[0], /уровне 2 из 5/);
    assert.ok(notes.some((body) => body.includes('**logic**')), 'сводка разбирающих этапов');
    assert.match(notes.at(-1), /<!-- llamer-status -->/);
    assert.match(notes.at(-1), new RegExp(`llamer-rev:${head}`));

    const resolve = writes.find((request) => request.method === 'PUT');
    assert.match(resolve.path, /\/discussions\/old-thread$/);
    assert.deepEqual(resolve.body, { resolved: true });

    const threads = writes.filter((request) => request.method === 'POST' && request.path.endsWith('/discussions'));
    assert.equal(threads.length, 1, 'повтор снят дедупликацией и до MR не доехал');
    const { position } = threads[0].body;
    assert.equal(position.new_path, 'src/Fresh.php');
    assert.equal(position.head_sha, head);
    assert.equal(position.line_range.start.type, 'new');
    assert.match(threads[0].body.body, /llamer-rev:/);

    // Заглушка драйвера напечатала токен GitLab в тексте находки.
    assert.match(threads[0].body.body, /токен \*\*\*REDACTED\*\*\*/);
    assert.ok(writes.every((request) => !JSON.stringify(request.body).includes(SECRETS.GITLAB_TOKEN)), 'токен ушёл в GitLab');
    // И в причине закрытия треда, которая идёт в лог.
    assert.match(result.stderr, /закрыт тред .* токен \*\*\*REDACTED\*\*\*/);
    assert.ok(!result.stderr.includes(SECRETS.GITLAB_TOKEN), 'токен в stderr');
  } finally {
    await gitlab.close();
  }
});

test('--publish gitlab --dry-run ничего не пишет и отдаёт отчёт', async () => {
  const { root, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr, '--publish', 'gitlab', '--dry-run']);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).findings.length, 1);
    assert.ok(gitlab.requests.every((request) => request.method === 'GET'));
  } finally {
    await gitlab.close();
  }
});

test('--mr без токена GitLab — пробел preflight, а не сетевой сбой посреди прогона', async () => {
  const { root, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr, '--preflight'], { GITLAB_TOKEN: '' });

    assert.equal(result.status, 1);
    assert.deepEqual(JSON.parse(result.stdout).gaps.map((gap) => gap.id), ['gitlab-token']);
    assert.equal(gitlab.requests.length, 0);
  } finally {
    await gitlab.close();
  }
});
