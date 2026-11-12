import { cpSync, existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { GitlabApi, MergeRequest, parseMrRef } from './gitlab/MergeRequest.mjs';
import { Diff } from './materials/Diff.mjs';
import { MACHINE_AUTHOR } from './materials/taskChanges.mjs';
import {
  git, tryGit, currentHead, noCommonHistory,
} from './git.mjs';
import { log } from './log.mjs';

/** Вход прогона не годится для разбора: причину читает человек, а не стек. */
export class InputError extends Error {}

function resolveRevision(rev, flag, cwd) {
  const sha = tryGit(['rev-parse', '--verify', '--quiet', `${rev}^{commit}`], cwd);
  if (!sha) throw new InputError(`${flag} ${rev}: такой ревизии в репозитории нет`);
  return sha;
}

/**
 * Merge request в GitLab: `--mr`. Треды и публикация — только у него. Из разбора
 * исключается целевая ветка в том виде, в каком она лежит в origin, и `--exclude`.
 */
export class GitlabRequest {
  #mr; #info; #excludes;

  constructor({ mr, info, excludes }) {
    this.#mr = mr;
    this.#info = info;
    this.#excludes = excludes;
  }

  static async open({
    ref, env, cwd, exclude,
  }) {
    const { url, projectPath, iid } = parseMrRef(ref, env);
    const mr = new MergeRequest({ api: new GitlabApi({ url, token: env.GITLAB_TOKEN }), projectPath, iid });
    const info = await mr.info();
    log(`MR !${iid} «${info.title}» (${info.sourceBranch} → ${info.targetBranch})`);
    if (!info.baseSha || !info.headSha) throw new InputError(`у MR !${iid} нет diff_refs — разбирать нечего`);
    const excludes = [`origin/${info.targetBranch}`, ...exclude];
    resolveRevision(excludes[0], 'целевая ветка MR', cwd);
    for (const rev of exclude) resolveRevision(rev, '--exclude', cwd);
    return new GitlabRequest({ mr, info, excludes });
  }

  get kind() { return 'gitlab'; }

  get mr() { return this.#mr; }

  get sourceBranch() { return this.#info.sourceBranch; }

  get targetBranch() { return this.#info.targetBranch; }

  get baseSha() { return this.#info.baseSha; }

  get headSha() { return this.#info.headSha; }

  get excludes() { return this.#excludes; }

  get explicitBase() { return null; }

  get label() { return `MR !${this.#mr.iid} ${this.sourceBranch} → ${this.targetBranch}`; }

  // GitLab сводит ветку с целевой сам; локальный git diff от базы MR на ветке
  // с влитой целевой тянет чужие правки.
  async requestDiff(cwd) {
    try {
      const files = await this.#mr.mergeDiff();
      log(`дифф реквеста от GitLab: ${files.length} файлов`);
      return Diff.fromMergeDiff({ files, baseRef: this.baseSha, cwd });
    } catch (error) {
      log(`дифф у GitLab не спросился (${String(error.message).split('\n')[0]})`
        + ' — считаем локально от базы MR');
      return Diff.overRange({ baseRef: this.baseSha, revs: [this.baseSha, this.headSha], cwd });
    }
  }

  discussions() { return this.#mr.discussions(); }
}

/**
 * Снимок коммитом поверх HEAD: индекса (`index`) или рабочего дерева с
 * неотслеживаемыми (`worktree`). Индекс копируется во временный файл, чтобы ни
 * `git add -A`, ни `write-tree` не трогали индекс человека. Копия сохраняет время
 * изменения: по нему git узнаёт записи, изменённые в ту же секунду, что и индекс,
 * и перечитывает их — со свежим временем правка той же длины потерялась бы.
 */
export function snapshot(cwd, what) {
  const dir = mkdtempSync(join(tmpdir(), 'llamer-index-'));
  const index = join(dir, 'index');
  const own = resolve(cwd, git(['rev-parse', '--git-path', 'index'], cwd));
  if (existsSync(own)) cpSync(own, index, { preserveTimestamps: true });
  const env = { ...MACHINE_AUTHOR, GIT_INDEX_FILE: index };
  try {
    if (what === 'worktree') git(['add', '-A', '--', ':/'], cwd, env);
    const tree = git(['write-tree'], cwd, env);
    return git(['commit-tree', tree, '-p', 'HEAD', '-m', `llamer: снимок ${what === 'worktree' ? 'рабочего дерева' : 'индекса'}`], cwd, env);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const SNAPSHOTS = ['worktree', 'index'];

/**
 * Ревизии репозитория: `--head` и `--base` или `--exclude`. С `--base` сравнение —
 * от неё как есть; с `--exclude` дифф реквеста считается от точки расхождения с
 * первой исключённой ревизией, а разбор — от всех.
 */
export class RevisionRequest {
  #data;

  constructor(data) {
    this.#data = data;
  }

  static open({
    cwd, base, head, exclude,
  }) {
    const headSha = SNAPSHOTS.includes(head) ? snapshot(cwd, head) : resolveRevision(head, '--head', cwd);
    for (const rev of exclude) resolveRevision(rev, '--exclude', cwd);
    let baseSha;
    if (base) {
      baseSha = resolveRevision(base, '--base', cwd);
    } else {
      baseSha = tryGit(['merge-base', headSha, exclude[0]], cwd);
      if (!baseSha) throw new InputError(noCommonHistory(headSha, exclude[0], cwd));
    }
    log(`голова ${head} (${headSha.slice(0, 8)}), ${base ? `база ${base}` : `без ${exclude.join(', ')}`}`);
    return new RevisionRequest({
      head, base, exclude, headSha, baseSha,
    });
  }

  get kind() { return 'revisions'; }

  get baseSha() { return this.#data.baseSha; }

  get headSha() { return this.#data.headSha; }

  get excludes() { return this.#data.exclude; }

  get explicitBase() { return this.#data.base ? this.#data.baseSha : null; }

  /** Голова — само рабочее дерево: этапы ходят по нему, отдельная выкладка не нужна. */
  get workTree() { return this.#data.head === 'worktree'; }

  get label() {
    const { head, base, exclude } = this.#data;
    return base ? `${base}..${head}` : `${head} без ${exclude.join(', ')}`;
  }

  async requestDiff(cwd) {
    return Diff.overRange({ baseRef: this.baseSha, revs: [this.baseSha, this.headSha], cwd });
  }

  async discussions() { return []; }
}

/**
 * Дерево, по которому ходят этапы, — с файлами головы разбора. Рабочая копия
 * годится, если стоит на ней; иначе голова выкладывается отдельным worktree во
 * временный каталог (объекты общие с основным `.git`) и убирается после прогона.
 */
export function openTree({ request, root }) {
  if (request.workTree || currentHead(root) === request.headSha) {
    return { path: root, close() {} };
  }
  if (tryGit(['cat-file', '-e', `${request.headSha}^{commit}`], root) === null) {
    throw new InputError(`коммита ${request.headSha.slice(0, 8)} в репозитории нет — сделай git fetch`);
  }
  const path = mkdtempSync(join(tmpdir(), 'llamer-tree-'));
  // Без hooksPath post-checkout-хук (lfs и подобные) может вернуть ненулевой код,
  // и выкладка сочтётся упавшей.
  git(['-c', 'core.hooksPath=/dev/null', 'worktree', 'add', '--detach', path, request.headSha], root);
  log(`голова ${request.headSha.slice(0, 8)} выложена в ${path}`);
  return {
    path,
    close() {
      tryGit(['worktree', 'remove', '--force', path], root);
      rmSync(path, { recursive: true, force: true });
    },
  };
}
