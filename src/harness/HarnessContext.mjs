import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { readRunState } from '../domain/journal.mjs';
import { Diff } from '../materials/Diff.mjs';

/** Зависимости команд — берутся по первому обращению, чтобы `stop` работал и без диффа. */
export class HarnessContext {
  #brief; #journal; #cwd; #cache = new Map();

  constructor({ brief, journal, cwd }) {
    this.#brief = brief;
    this.#journal = journal;
    this.#cwd = cwd;
  }

  get stage() { return this.#brief.stage; }

  get name() { return this.#brief.name; }

  get commands() { return this.#brief.commands; }

  get relatedRequired() { return this.#brief.relatedRequired; }

  get journal() { return this.#journal; }

  get state() {
    return this.#once('state', () => readRunState(this.#brief.journalDir).hideStages(this.#brief.siblings));
  }

  get diff() {
    return this.#once('diff', () => Diff.fromText(readFileSync(this.#brief.diffPath, 'utf-8'), { baseRef: this.#brief.baseSha }));
  }

  get openThreads() { return this.#brief.threads; }

  readFileLines(file) {
    return readFileSync(join(this.#cwd, file), 'utf-8').split('\n');
  }

  #once(key, build) {
    if (!this.#cache.has(key)) this.#cache.set(key, build());
    return this.#cache.get(key);
  }
}
