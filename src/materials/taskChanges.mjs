import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Diff } from './Diff.mjs';
import { log } from '../log.mjs';
import {
  git, tryGit, isAncestor, firstLine, noCommonHistory,
} from '../git.mjs';

/** Изменения задачи вычислить не удалось — ревью без них не запускается. */
export class TaskChangesError extends Error {}

// На перекрёстных слияниях общих предков несколько, и любой один теряет то, что
// есть только в других (влитую в целевую ветку подзадачу): берутся все.
function mergeBasesWith(head, rev, cwd) {
  const shas = tryGit(['merge-base', '--all', head, rev], cwd);
  if (!shas) throw new TaskChangesError(noCommonHistory(head, rev, cwd));
  return shas.split('\n');
}

/**
 * Сливает две ревизии без рабочего дерева. merge-base для них git выбирает
 * сам: явно заданный ломается на перекрёстных слияниях и возвращает в базу
 * сотни чужих файлов.
 */
function mergeTree(ours, theirs, cwd) {
  try {
    return { tree: git(['merge-tree', '--write-tree', '--name-only', '--no-messages', ours, theirs], cwd), conflicted: [] };
  } catch (error) {
    // Код 1 — слияние с конфликтами: дерево записано, ниже него конфликтные пути.
    if (error.status !== 1) {
      throw new TaskChangesError(`слияние ${ours.slice(0, 8)} и ${theirs.slice(0, 8)} не собралось`
        + ` (${firstLine(error)})`);
    }
    const [tree, ...conflicted] = String(error.stdout).trim().split('\n').filter(Boolean);
    return { tree, conflicted };
  }
}

/**
 * Конфликтный путь в слитом дереве содержит маркеры конфликта. Такой файл
 * берётся в базу из базы реквеста — по нему ревьюится дифф реквеста: лишний
 * чужой код в нём дешевле, чем пропущенный свой.
 */
function takeFromRequestBase(tree, paths, requestBase, cwd) {
  const dir = mkdtempSync(join(tmpdir(), 'llamer-base-'));
  const env = { GIT_INDEX_FILE: join(dir, 'index') };
  try {
    git(['read-tree', tree], cwd, env);
    for (const path of paths) {
      const entry = git(['ls-tree', requestBase, '--', path], cwd);
      if (entry) {
        const [meta] = entry.split('\t');
        const [mode, , sha] = meta.split(' ');
        git(['update-index', '--add', '--cacheinfo', `${mode},${sha},${path}`], cwd, env);
      } else {
        git(['update-index', '--force-remove', '--', path], cwd, env);
      }
    }
    return git(['write-tree'], cwd, env);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

export const MACHINE_AUTHOR = {
  GIT_AUTHOR_NAME: 'llamer',
  GIT_AUTHOR_EMAIL: 'llamer@localhost',
  GIT_COMMITTER_NAME: 'llamer',
  GIT_COMMITTER_EMAIL: 'llamer@localhost',
};

/**
 * Виртуальная база — всё, что уже есть в исключённых ревизиях, в одном коммите.
 * Коммит, а не голое дерево: этапы читают базу через `git show <sha>:<путь>`.
 */
function virtualBase(bases, requestBase, cwd) {
  let head = bases[0].sha;
  const conflicted = new Set();
  for (const { sha } of bases.slice(1)) {
    if (isAncestor(sha, head, cwd)) continue;
    if (isAncestor(head, sha, cwd)) {
      head = sha;
      continue;
    }
    const merged = mergeTree(head, sha, cwd);
    for (const path of merged.conflicted) conflicted.add(path);
    const tree = merged.conflicted.length > 0
      ? takeFromRequestBase(merged.tree, [...conflicted], requestBase, cwd)
      : merged.tree;
    head = git(['commit-tree', '-p', head, '-p', sha, '-m', 'llamer: база изменений задачи', tree], cwd, MACHINE_AUTHOR);
  }
  return { sha: head, conflicted: [...conflicted].sort() };
}

/**
 * Метка прошлого прогона, от которой считать: последняя из тех, чья ревизия есть
 * в истории головы. Метка вне истории — голова переписана (force-push, rebase).
 * Совпавшая с базой реквеста ничего не сужает.
 */
function lastReviewed(marks, head, requestBase, cwd) {
  const inHistory = marks.filter((sha) => isAncestor(sha, head, cwd));
  const dropped = marks.filter((sha) => !inHistory.includes(sha));
  if (dropped.length > 0) {
    log(`--since-last: метки вне истории головы пропущены (force-push или rebase): ${dropped.map((sha) => sha.slice(0, 8)).join(', ')}`);
  }
  if (inHistory.length === 0) {
    log('--since-last: годной метки прошлого прогона нет — разбирается весь реквест');
    return null;
  }
  const sha = inHistory.reduce((a, b) => (isAncestor(a, b, cwd) ? b : a));
  if (sha === requestBase) {
    log(`--since-last: метка ${sha.slice(0, 8)} совпадает с базой реквеста — разбирается весь реквест`);
    return null;
  }
  log(`--since-last: только изменения после прошлого прогона ${sha.slice(0, 8)}`);
  return sha;
}

function describe(changes) {
  const bases = changes.bases.map(({ rev, sha }) => `${rev}@${sha.slice(0, 8)}`).join(', ');
  log(`изменения задачи от ${bases}: ${changes.diff.files.length} файлов,`
    + ` отсеяно чужих ${changes.excluded.length}`
    + (changes.conflicted.length > 0 ? `, конфликтных по диффу реквеста ${changes.conflicted.length}` : ''));
}

/**
 * Изменения задачи — то, чего ещё нет ни в одной из исключённых ревизий
 * (`excludes`: целевая ветка MR и `--exclude`). Ветка задачи может ответвиться от
 * одной общей ветки, а влиться в другую, и дифф реквеста несёт всё, что есть в
 * исходной и ещё не пришло в целевую: чужой код, уже прошедший ревью. Работа
 * автора — коммиты, разрешения конфликтов и правки поверх влитых веток — это то,
 * что голова добавляет к слиянию этих ревизий.
 *
 * `explicitBase` (`--base`) заменяет вычисление: сравнение идёт от неё одной.
 *
 * `sinceMarks` (`--since-last`) — метки прошлых прогонов: годная становится ещё
 * одной базой, и уже разобранное отсеивается так же, как исключённые ревизии.
 *
 * @returns {{
 *   kind: 'task' | 'mixed' | 'explicit',
 *   bases: {rev: string, sha: string}[],
 *   baseSha: string,
 *   diff: Diff,
 *   conflicted: string[],
 *   excluded: string[],
 *   since: string | null,
 * }}
 */
export function taskChanges(args) {
  try {
    return computeTaskChanges(args);
  } catch (error) {
    if (error instanceof TaskChangesError) throw error;
    throw new TaskChangesError(`git не посчитал изменения задачи (${firstLine(error)})`);
  }
}

function computeTaskChanges({
  cwd, head, excludes = [], requestDiff, requestBase, explicitBase = null, sinceMarks = null,
}) {
  let bases;
  let base;
  let since = null;
  if (explicitBase) {
    bases = [{ rev: '--base', sha: explicitBase }];
    base = { sha: explicitBase, conflicted: [] };
  } else {
    bases = [...new Set(excludes)].flatMap((rev) => mergeBasesWith(head, rev, cwd).map((sha) => ({ rev, sha })));
    since = sinceMarks ? lastReviewed(sinceMarks, head, requestBase, cwd) : null;
    if (since) bases.push({ rev: 'прошлый прогон', sha: since });
    base = virtualBase(bases, requestBase, cwd);
  }

  const diff = Diff.overRange({ baseRef: base.sha, revs: [base.sha, head], cwd });
  const own = new Set(diff.files);
  const changes = {
    kind: explicitBase ? 'explicit' : (base.conflicted.length > 0 ? 'mixed' : 'task'),
    bases,
    baseSha: base.sha,
    diff,
    conflicted: base.conflicted.filter((path) => own.has(path)),
    excluded: requestDiff.files.filter((file) => !own.has(file)),
    since,
  };
  describe(changes);
  return changes;
}
