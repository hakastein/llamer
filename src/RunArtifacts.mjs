import {
  createWriteStream, mkdirSync, readdirSync, readFileSync, writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { writeCostJson, formatCostTable } from './agent/cost.mjs';
import { redactSecrets } from './redact.mjs';
import { Journal } from './domain/journal.mjs';
import { log } from './log.mjs';

export const ARTIFACTS_DIR = '.llamer';

export function stageSlug(name) {
  return name.replace(/[^\wА-яЁё]+/gu, '-').replace(/^-|-$/g, '');
}

/**
 * Файловая раскладка одного прогона: журналы этапов, задания, промпты, сырые
 * потоки, стоимость и отчёт — всё под одним каталогом со штампом времени.
 */
export class RunArtifacts {
  #path; #journalDir; #briefsDir;

  constructor({ path, journalDir, briefsDir }) {
    this.#path = path;
    this.#journalDir = journalDir;
    this.#briefsDir = briefsDir;
  }

  get path() { return this.#path; }

  openJournal(name) {
    return new Journal(this.#journalDir, name);
  }

  /**
   * Без обработчика 'error' (ENOSPC/EACCES в каталоге артефактов) поток убил бы
   * весь прогон необработанным событием. Сырой поток потерять не жалко —
   * разбор и публикация от него не зависят.
   */
  openStream(stageName) {
    const stream = createWriteStream(join(this.#path, `${stageSlug(stageName)}.jsonl`));
    stream.on('error', (error) => {
      log(`[${stageName}] сырой поток этапа не пишется (${error.message}) — в артефакты не ляжет`);
    });
    return {
      write: (event) => stream.write(`${redactSecrets(JSON.stringify(event))}\n`),
      end: () => stream.end(),
    };
  }

  savePrompt(stageName, text) {
    const path = join(this.#path, `${stageSlug(stageName)}.prompt.md`);
    try {
      writeFileSync(path, redactSecrets(text), 'utf-8');
    } catch (error) {
      log(`[${stageName}] задание не сохранено (${error.message})`);
    }
    return path;
  }

  saveBrief(stageKey, brief) {
    const path = join(this.#briefsDir, `${stageKey}.json`);
    writeFileSync(path, `${JSON.stringify(brief, null, 2)}\n`, 'utf-8');
    return path;
  }

  saveDiff(name, text) {
    const path = join(this.#path, `${name}.diff`);
    writeFileSync(path, text, 'utf-8');
    return path;
  }

  saveText(name, text) {
    const path = join(this.#path, name);
    writeFileSync(path, redactSecrets(text), 'utf-8');
    return path;
  }

  /**
   * Журналы пишет харнесс в окружении этапа, где секретов llamer нет, — и
   * вымарать их там нечем. Вымарываются после прогона, когда этапы уже не пишут.
   */
  redactJournals() {
    for (const name of readdirSync(this.#journalDir).filter((file) => file.endsWith('.jsonl'))) {
      const path = join(this.#journalDir, name);
      const text = readFileSync(path, 'utf-8');
      const redacted = redactSecrets(text);
      if (redacted !== text) writeFileSync(path, redacted, 'utf-8');
    }
  }

  reportCost(records, elapsedMs) {
    if (records.length === 0) return;
    for (const line of formatCostTable(records, { elapsedMs }).split('\n')) log(line);
    const path = join(this.#path, 'cost.json');
    writeCostJson({ path, stages: records });
    log(`стоимость по этапам: ${path}`);
  }
}

/**
 * `.gitignore` со звёздочкой внутри каталога артефактов: снимок рабочего дерева
 * (`--local`) и поиск по коду иначе захватили бы артефакты прошлых прогонов.
 */
export function openRunArtifacts({ root, startedAt }) {
  const stamp = new Date(startedAt).toISOString().slice(0, 19)
    .replace('T', '_')
    .replace(/:/g, '-');
  const base = join(root, ARTIFACTS_DIR);
  mkdirSync(base, { recursive: true });
  writeFileSync(join(base, '.gitignore'), '*\n');
  const path = join(base, stamp);
  const journalDir = join(path, 'journal');
  const briefsDir = join(path, 'briefs');
  mkdirSync(journalDir, { recursive: true });
  mkdirSync(briefsDir, { recursive: true });
  return new RunArtifacts({ path, journalDir, briefsDir });
}
