import { reviewableThreads } from './threads.mjs';
import { matchesAnyMask } from './pathMask.mjs';

export class Materials {
  #data;

  constructor(data) {
    this.#data = { threads: [], ...data };
  }

  get diff() { return this.#data.diff; }

  get signals() { return this.#data.signals; }

  get issueId() { return this.#data.issueId; }

  get threads() { return this.#data.threads; }

  get requestDiff() { return this.#data.requestDiff; }

  get diffPath() { return this.#data.diffPath; }

  /** Изменения задачи: база, отсеянное, конфликтное. */
  get task() { return this.#data.task; }

  /** Изменения задачи с контекстом GitLab: по ним удалённая строка находит строку реквеста. */
  get taskAnchors() { return this.#data.taskAnchors; }

  get cwd() { return this.#data.cwd; }

  get files() { return this.diff.files; }

  // База изменений задачи: по ней пронумерована старая сторона диффа этапов, из
  // неё же этап читает `git show <base>:<file>`.
  get baseSha() { return this.task.baseSha; }

  /** Файлы под маски этапа; без масок — все. */
  filesMatching(paths) {
    return this.files.filter((file) => matchesAnyMask(paths, [file]));
  }

  get openThreads() {
    return reviewableThreads(this.threads)
      .map((thread) => ({ ...thread, at: `${thread.file}:${thread.line}` }));
  }

  /** Дифф и треды — одни на все этапы прогона. */
  get sharedPack() { return this.#data.pack.shared(); }

  /** Часть пакета, своя у этапа: задача, со ссылкой на общую часть. */
  renderPack(options) { return this.#data.pack.forStage(options); }
}
