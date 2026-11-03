import { cpSync, existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { GitlabApi, MergeRequest, parseMrRef } from './gitlab/MergeRequest.mjs';
import { Diff } from './materials/Diff.mjs';
import { MACHINE_AUTHOR, branchRef } from './materials/taskChanges.mjs';
import { git, tryGit, currentHead } from './git.mjs';
import { log } from './log.mjs';

/** Вход прогона не годится для разбора: причину читает человек, а не стек. */
export class InputError extends Error {}

function currentBranch(cwd) {
  return tryGit(['symbolic-ref', '--quiet', '--short', 'HEAD'], cwd);
}

/** Merge request в GitLab: `--mr`. Треды и публикация — только у него. */
export class GitlabRequest {
  #mr; #info;

  constructor({ mr, info }) {
    this.#mr = mr;
    this.#info = info;
  }

  static async open({ ref, env }) {
    const { url, projectPath, iid } = parseMrRef(ref, env);
    const mr = new MergeRequest({ api: new GitlabApi({ url, token: env.GITLAB_TOKEN }), projectPath, iid });
    const info = await mr.info();
    log(`MR !${iid} «${info.title}» (${info.sourceBranch} → ${info.targetBranch})`);
    if (!info.baseSha || !info.headSha) throw new InputError(`у MR !${iid} нет diff_refs — разбирать нечего`);
    return new GitlabRequest({ mr, info });
  }

  get kind() { return 'gitlab'; }

  get mr() { return this.#mr; }

  get sourceBranch() { return this.#info.sourceBranch; }

  get targetBranch() { return this.#info.targetBranch; }

  get baseSha() { return this.#info.baseSha; }

  get headSha() { return this.#info.headSha; }

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
 * Снимок рабочего дерева коммитом: в разбор идут и незакоммиченные, и
 * неотслеживаемые правки. Индекс копируется во временный файл, чтобы `git add -A`
 * не трогал staged-правки человека. Копия сохраняет время изменения: по нему git
 * узнаёт записи, изменённые в ту же секунду, что и индекс, и перечитывает их —
 * со свежим временем правка той же длины потерялась бы.
 */
export function snapshotWorkTree(cwd) {
  const dir = mkdtempSync(join(tmpdir(), 'llamer-index-'));
  const index = join(dir, 'index');
  const own = resolve(cwd, git(['rev-parse', '--git-path', 'index'], cwd));
  if (existsSync(own)) cpSync(own, index, { preserveTimestamps: true });
  const env = { ...MACHINE_AUTHOR, GIT_INDEX_FILE: index };
  try {
    git(['add', '-A', '--', ':/'], cwd, env);
    const tree = git(['write-tree'], cwd, env);
    return git(['commit-tree', tree, '-p', 'HEAD', '-m', 'llamer: снимок рабочего дерева'], cwd, env);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// Без --target берётся ветка, на которую смотрит origin/HEAD, а без неё — main.
function defaultTarget(cwd) {
  return tryGit(['symbolic-ref', '--quiet', '--short', 'refs/remotes/origin/HEAD'], cwd)?.replace(/^origin\//, '') ?? 'main';
}

/** Рабочее дерево против целевой ветки: `--local [--target]`. GitLab не нужен. */
export class LocalRequest {
  #data;

  constructor(data) {
    this.#data = data;
  }

  static open({ cwd, target = null }) {
    const targetBranch = target ?? defaultTarget(cwd);
    const branch = currentBranch(cwd);
    const targetRef = branchRef(targetBranch, cwd);
    if (!targetRef) {
      throw new InputError(`целевой ветки ${targetBranch} нет ни в origin, ни локально — передай --target <ветка>`);
    }
    if (branch === targetBranch) {
      throw new InputError(`ветка ${branch} и есть целевая: разбирать нечего`
        + ' — переключись на свою ветку или передай --target <ветка>');
    }
    const baseSha = tryGit(['merge-base', targetRef, 'HEAD'], cwd);
    if (!baseSha) throw new InputError(`у HEAD и ${targetRef} нет общей истории — передай --target <ветка>`);
    const headSha = snapshotWorkTree(cwd);
    log(`ветка ${branch ?? '(detached HEAD)'} → ${targetBranch}, база ${baseSha.slice(0, 8)},`
      + ` снимок рабочего дерева ${headSha.slice(0, 8)}`);
    return new LocalRequest({
      sourceBranch: branch, targetBranch, baseSha, headSha,
    });
  }

  get kind() { return 'local'; }

  get sourceBranch() { return this.#data.sourceBranch; }

  get targetBranch() { return this.#data.targetBranch; }

  get baseSha() { return this.#data.baseSha; }

  get headSha() { return this.#data.headSha; }

  get label() { return `${this.sourceBranch ?? 'detached HEAD'} → ${this.targetBranch} (рабочее дерево)`; }

  async requestDiff(cwd) {
    return Diff.overRange({ baseRef: this.baseSha, revs: [this.baseSha, this.headSha], cwd });
  }

  async discussions() { return []; }
}

/** Две ревизии репозитория: `--base <sha> --head <sha>`. Сравнение — от базы как есть. */
export class RangeRequest {
  #data;

  constructor(data) {
    this.#data = data;
  }

  static open({ cwd, base, head }) {
    const resolveSha = (rev, flag) => {
      const sha = tryGit(['rev-parse', '--verify', '--quiet', `${rev}^{commit}`], cwd);
      if (!sha) throw new InputError(`${flag} ${rev}: такой ревизии в репозитории нет`);
      return sha;
    };
    return new RangeRequest({
      sourceBranch: currentBranch(cwd),
      baseSha: resolveSha(base, '--base'),
      headSha: resolveSha(head, '--head'),
    });
  }

  get kind() { return 'range'; }

  get sourceBranch() { return this.#data.sourceBranch; }

  get targetBranch() { return null; }

  get baseSha() { return this.#data.baseSha; }

  get headSha() { return this.#data.headSha; }

  get label() { return `${this.baseSha.slice(0, 8)}..${this.headSha.slice(0, 8)}`; }

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
  if (request.kind === 'local' || currentHead(root) === request.headSha) {
    return { path: root, close() {} };
  }
  if (tryGit(['cat-file', '-e', `${request.headSha}^{commit}`], root) === null && request.kind === 'gitlab') {
    tryGit(['fetch', '--quiet', 'origin', request.sourceBranch], root);
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
