import { test } from 'node:test';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { parse } from 'yaml';
import {
  git, makeRepo, run, CONFIG, SECRETS, WORKTREE,
} from './fixtures/repo.mjs';
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

// Реквест из настоящего репозитория: дерево стоит на голове ветки, как клон CI,
// целевая ветка — в origin, куда её фетчит вызывающий.
// Наш открытый тред прошлого прогона висит на src/Order.php — его снимет этап тредов.
// `markOf` даёт ревизию прошлого прогона: она встаёт меткой в заметку-статус.
async function pipeline({ markOf = null, config = CONFIG } = {}) {
  const root = makeRepo({ config });
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'finish');
  const base = git(root, 'merge-base', 'main', 'HEAD');
  const head = git(root, 'rev-parse', 'HEAD');
  git(root, 'update-ref', 'refs/remotes/origin/main', 'main');
  const mark = markOf ? markOf(root) : null;
  const statusNotes = mark ? [{
    id: 'last-status',
    individual_note: true,
    notes: [{ body: `<!-- llamer-status -->\nСтатус.\n\n<sub>ревизия</sub><!-- llamer-rev:${mark} -->`, author: { username: 'bot' } }],
  }] : [];
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
    }, ...statusNotes],
  });
  return {
    root, base, head, mark, gitlab, mr: `${gitlab.url}/${PROJECT}/-/merge_requests/${IID}`,
  };
}

test('--mr без публикации: отчёт по MR, в GitLab только чтение', async () => {
  const { root, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr]);

    assert.equal(result.status, 0, result.stderr);
    const answer = parse(result.stdout);
    assert.equal(answer.input, 'gitlab');
    assert.equal(answer.findings.length, 1);
    assert.deepEqual(answer.resolvedThreads.map((thread) => [thread.id, thread.place]), [['old-thread', 'src/Order.php:4']]);
    assert.match(readFileSync(answer.report, 'utf-8'), /## Снятые треды\n\n- src\/Order\.php:4 — /);
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
    assert.match(result.stderr, /закрыт тред src\/Order\.php:4 — .* токен \*\*\*REDACTED\*\*\*/);
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
    assert.equal(parse(result.stdout).findings.length, 1);
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
    assert.deepEqual(parse(result.stdout).gaps.map((gap) => gap.id), ['gitlab-token']);
    assert.equal(gitlab.requests.length, 0);
  } finally {
    await gitlab.close();
  }
});

test('--since-last: метка в истории головы — разбирается только дельта после неё', async () => {
  const { root, mark, gitlab, mr } = await pipeline({ markOf: (repo) => git(repo, 'rev-parse', 'HEAD~1') });
  try {
    const result = await run(root, ['--mr', mr, '--since-last']);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(parse(result.stdout).base, mark, 'изменения задачи считаются от метки');
    assert.match(result.stderr, new RegExp(`только изменения после прошлого прогона ${mark.slice(0, 8)}`));
  } finally {
    await gitlab.close();
  }
});

test('--since-last: метка вне истории головы пропущена с записью в логе — весь реквест', async () => {
  const { root, base, mark, gitlab, mr } = await pipeline({ markOf: (repo) => git(repo, 'rev-parse', 'main') });
  try {
    const result = await run(root, ['--mr', mr, '--since-last']);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(parse(result.stdout).base, base);
    assert.match(result.stderr, new RegExp(`вне истории головы пропущены.*${mark.slice(0, 8)}`));
  } finally {
    await gitlab.close();
  }
});

test('--since-last: метки в заметке-статусе нет — весь реквест', async () => {
  // Метка на нашем треде — не метка прогона: в счёт идёт только заметка-статус.
  const { root, base, gitlab, mr } = await pipeline();
  try {
    const result = await run(root, ['--mr', mr, '--since-last']);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(parse(result.stdout).base, base);
    assert.match(result.stderr, /годной метки прошлого прогона нет/);
  } finally {
    await gitlab.close();
  }
});

test('--since-last: после прошлого прогона нового нет — этапы не поднимаются, в MR ничего не пишется', async () => {
  const { root, head, gitlab, mr } = await pipeline({ markOf: (repo) => git(repo, 'rev-parse', 'HEAD') });
  try {
    const result = await run(root, ['--mr', mr, '--since-last']);

    assert.equal(result.status, 0, result.stderr);
    const answer = parse(result.stdout);
    assert.equal(answer.base, head);
    assert.deepEqual(answer.findings, []);
    assert.equal(result.launches.length, 0);

    // С публикацией — только строка в логе: статус прошлого прогона остаётся в силе.
    const published = await run(root, ['--mr', mr, '--since-last', '--publish', 'gitlab']);
    assert.equal(published.status, 0, published.stderr);
    assert.match(published.stderr, /новых своих изменений нет — в MR ничего не публикуется/);
    assert.ok(gitlab.requests.every((request) => request.method === 'GET'), 'в MR ничего не записано');
  } finally {
    await gitlab.close();
  }
});

test('--since-last: этап сорвался — статус без метки, следующий прогон разбирает весь реквест', async () => {
  const config = { ...CONFIG, drivers: { claude: { env: { STUB_FAIL_STAGE: 'logic' } } } };
  const { root, base, gitlab, mr } = await pipeline({ config });
  try {
    const failed = await run(root, ['--mr', mr, '--since-last', '--publish', 'gitlab']);
    assert.equal(failed.status, 1, 'сорванный этап — неуспешный прогон');
    assert.match(failed.stderr, /этап «logic» сорвался/);
    const status = gitlab.requests.find((request) => request.method === 'POST' && request.body.body?.startsWith('<!-- llamer-status -->'));
    assert.ok(status, 'статус опубликован');
    assert.doesNotMatch(status.body.body, /llamer-rev/);

    const next = await run(root, ['--mr', mr, '--since-last']);
    assert.equal(parse(next.stdout).base, base, 'неразобранное после срыва не пропущено');
    assert.match(next.stderr, /годной метки прошлого прогона нет/);
  } finally {
    await gitlab.close();
  }
});

test('--since-last: этап прекратил работу — статус без метки, следующий прогон разбирает весь реквест', async () => {
  const config = { ...CONFIG, drivers: { claude: { env: { STUB_STOP_STAGE: 'logic' } } } };
  const { root, base, gitlab, mr } = await pipeline({ config });
  try {
    const stopped = await run(root, ['--mr', mr, '--since-last', '--publish', 'gitlab']);
    assert.equal(stopped.status, 0, stopped.stderr);
    const status = gitlab.requests.find((request) => request.method === 'POST' && request.body.body?.startsWith('<!-- llamer-status -->'));
    assert.ok(status, 'статус опубликован');
    assert.doesNotMatch(status.body.body, /llamer-rev/);

    const next = await run(root, ['--mr', mr, '--since-last']);
    assert.equal(parse(next.stdout).base, base, 'неразобранное после stop не пропущено');
    assert.match(next.stderr, /годной метки прошлого прогона нет/);
  } finally {
    await gitlab.close();
  }
});

test('--since-last без --mr — ошибка аргументов до прогона', async () => {
  const result = await run(makeRepo(), [...WORKTREE, '--since-last']);

  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /--since-last — только вместе с --mr/);
});
