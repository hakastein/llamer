import { readFileSync } from 'node:fs';

export const BRIEF_ENV = 'LLAMER_BRIEF';

/** Задание этапа — JSON-файл, путь до него приходит переменной окружения. */
export class StageBrief {
  #data;

  constructor(data) {
    for (const field of ['stage', 'journalDir', 'diffPath']) {
      if (!data[field]) throw new Error(`задание этапа неполно: нет поля ${field}`);
    }
    this.#data = data;
  }

  static read(env) {
    const path = env[BRIEF_ENV];
    if (!path) throw new Error(`${BRIEF_ENV} не задан: команда запущена вне этапа ревью`);
    return new StageBrief(JSON.parse(readFileSync(path, 'utf-8')));
  }

  get stage() { return this.#data.stage; }

  get name() { return this.#data.name; }

  get driver() { return this.#data.driver; }

  get journalDir() { return this.#data.journalDir; }

  get baseSha() { return this.#data.baseSha; }

  get diffPath() { return this.#data.diffPath; }

  get revs() { return this.#data.revs; }

  get commands() { return this.#data.commands ?? []; }

  get siblings() { return this.#data.siblings ?? []; }

  /** Требовать ли у находки `--related`: этапу комментариев место докажет сам комментарий. */
  get relatedRequired() { return this.#data.relatedRequired !== false; }

  get threads() { return this.#data.threads ?? []; }
}
