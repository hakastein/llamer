import { git } from '../git.mjs';
import { widenFromWorkTree } from './widen.mjs';
import { fileReader } from './windows.mjs';

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

function sideOf(store, header, prefix) {
  const path = header.slice(4).trim();
  if (path === '/dev/null') return null;
  const file = path.replace(prefix, '');
  if (!store.has(file)) store.set(file, new Map());
  return store.get(file);
}

/**
 * Разбор `git diff -U0` за один проход по обеим сторонам.
 *
 * `added` — Map<новый путь, Map<новая строка, {oldLine, text}>>,
 * `removed` — Map<старый путь, Map<старая строка, {newLine, text}>>.
 *
 * Встречный номер хранится потому, что GitLab адресует строку парой «старый
 * номер, новый номер»: без неё многострочный тред создаётся, но подсветка
 * диапазона не встаёт.
 */
export function parseUnifiedDiff(diffText) {
  const added = new Map();
  const removed = new Map();
  let addedSide = null;
  let removedSide = null;
  let oldPos = 0;
  let newPos = 0;
  let inHunk = false;

  for (const line of diffText.split('\n')) {
    if (line.startsWith('diff --git ')) {
      inHunk = false;
      addedSide = null;
      removedSide = null;
      continue;
    }
    if (!inHunk && line.startsWith('--- ')) {
      removedSide = sideOf(removed, line, /^a\//);
      continue;
    }
    if (!inHunk && line.startsWith('+++ ')) {
      addedSide = sideOf(added, line, /^b\//);
      continue;
    }

    const hunk = HUNK.exec(line);
    if (hunk) {
      inHunk = Boolean(addedSide || removedSide);
      oldPos = Number(hunk[1]);
      newPos = Number(hunk[2]);
      continue;
    }
    if (!inHunk) continue;
    // «\ No newline at end of file» к нумерации отношения не имеет.
    if (line.startsWith('\\')) continue;

    if (line.startsWith('-')) {
      removedSide?.set(oldPos, { newLine: newPos, text: line.slice(1) });
      oldPos += 1;
    } else if (line.startsWith('+')) {
      addedSide?.set(newPos, { oldLine: oldPos, text: line.slice(1) });
      newPos += 1;
    } else {
      oldPos += 1;
      newPos += 1;
    }
  }

  return { added, removed };
}

export class Diff {
  #read = null;

  #widened = new Map();

  #cwd = null;

  // Путь до переименования: GitLab не принимает позицию, где old_path равен
  // new_path у переименованного файла, и вешает тред на реквест целиком.
  #renamedFrom = new Map();

  #sourceText = null;

  /**
   * `files` включает удалённые файлы: сторона `added` их не хранит, а список
   * служит ещё и pathspec'ом для широкого диффа — без них он по таким файлам пуст.
   */
  constructor({
    baseRef, files, added, removed, revs = null, cwd = null,
  }) {
    this.baseRef = baseRef;
    this.files = files;
    this.added = added;
    this.removed = removed;
    this.revs = revs ?? [baseRef, 'HEAD'];
    this.#cwd = cwd;
  }

  static overRange({ baseRef, revs, cwd }) {
    const files = git(['diff', '--name-only', ...revs], cwd)
      .split('\n')
      .filter((line) => line.trim() !== '');
    const narrow = git(['diff', '-U0', ...revs], cwd);
    const { added, removed } = parseUnifiedDiff(narrow);
    const diff = new Diff({
      baseRef, files, added, removed, revs, cwd,
    });
    // Узкий дифф уже посчитан — кладём в кэш ширин, чтобы `text({ context: 0 })`
    // не звал git второй раз за тем же самым.
    diff.#widened.set(0, narrow);
    return diff;
  }

  static fromMergeDiff({ files, baseRef, cwd }) {
    const text = files.map((file) => [
      `diff --git a/${file.oldPath} b/${file.path}`,
      file.born ? '--- /dev/null' : `--- a/${file.oldPath}`,
      file.deleted ? '+++ /dev/null' : `+++ b/${file.path}`,
      file.diff.replace(/\n$/, ''),
    ].join('\n')).join('\n');

    const diff = Diff.fromText(text, {
      baseRef,
      files: files.map((file) => (file.deleted ? file.oldPath : file.path)),
    });
    for (const file of files) {
      if (file.renamed && file.oldPath) diff.#renamedFrom.set(file.path, file.oldPath);
    }
    diff.#read = fileReader(cwd);
    diff.#cwd = cwd;
    diff.#widened.delete(0);
    return diff;
  }

  static fromText(diffText, { baseRef = null, files = null } = {}) {
    const { added, removed } = parseUnifiedDiff(diffText);
    const diff = new Diff({
      baseRef,
      files: files ?? [...new Set([...added.keys(), ...removed.keys()])],
      added,
      removed,
    });
    diff.#widened.set(0, diffText);
    diff.#sourceText = diffText;
    return diff;
  }

  oldPathOf(file) {
    return this.#renamedFrom.get(file) ?? file;
  }

  get sourceText() {
    return this.#sourceText;
  }

  get isEmpty() {
    return this.files.length === 0;
  }

  get changedLineCount() {
    let count = 0;
    for (const lines of this.added.values()) count += lines.size;
    return count;
  }

  side(deleted) {
    return deleted ? this.removed : this.added;
  }

  touches(file, { deleted = false } = {}) {
    return this.side(deleted).has(file);
  }

  hasLine(file, line, { deleted = false } = {}) {
    return Boolean(this.side(deleted).get(file)?.has(Number(line)));
  }

  linesOf(file, { deleted = false } = {}) {
    const lines = this.side(deleted).get(file);
    return lines ? [...lines.keys()] : [];
  }

  pairAt(file, line, { deleted = false } = {}) {
    const number = Number(line);
    const entry = this.side(deleted).get(file)?.get(number);
    if (!entry) return null;
    return deleted
      ? { oldLine: number, newLine: entry.newLine }
      : { oldLine: entry.oldLine, newLine: number };
  }

  text({ context = 10 } = {}) {
    if (this.files.length === 0) return '';
    if (!this.#widened.has(context)) {
      this.#widened.set(context, this.#read
        ? widenFromWorkTree({
          files: this.files, added: this.added, removed: this.removed, read: this.#read, context,
        })
        : git(['diff', `-U${context}`, ...this.revs, '--', ...this.files], this.#cwd));
    }
    return this.#widened.get(context);
  }
}
