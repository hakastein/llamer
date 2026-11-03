import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const TEMPLATES = join(dirname(fileURLToPath(import.meta.url)), 'templates');
const PLACEHOLDER = /\[([A-Z][A-Z0-9_]*)]/g;

// Незаполненная дырка уехала бы к модели текстом `[PACK]`, поэтому оба перекоса
// караются на сборке.
export class PromptTemplate {
  #key; #text;

  constructor(key) {
    this.#key = key;
    this.#text = readFileSync(join(TEMPLATES, `${key}.md`), 'utf-8');
  }

  get names() {
    return new Set([...this.#text.matchAll(PLACEHOLDER)].map(([, name]) => name));
  }

  render(values) {
    const wanted = this.names;
    const given = new Set(Object.keys(values));

    const missing = [...wanted].filter((name) => !given.has(name));
    if (missing.length > 0) {
      throw new Error(`шаблон ${this.#key}: нечем заполнить ${missing.join(', ')}`);
    }
    const extra = [...given].filter((name) => !wanted.has(name));
    if (extra.length > 0) {
      throw new Error(`шаблон ${this.#key}: некуда подставить ${extra.join(', ')}`);
    }

    return this.#text
      .replace(PLACEHOLDER, (_, name) => values[name])
      .replace(/\n{3,}/g, '\n\n')
      .trimEnd()
      .concat('\n');
  }
}
