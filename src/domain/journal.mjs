import {
  appendFileSync, existsSync, readdirSync, readFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { Refusal } from './Refusal.mjs';
import { Finding } from './Finding.mjs';

export const MAX_LINE_BYTES = 64 * 1024;

export function encodeEvent(event) {
  const line = JSON.stringify(event);
  const bytes = Buffer.byteLength(line, 'utf-8');
  if (bytes > MAX_LINE_BYTES) {
    // Refusal, а не Error: длину текста выбрал агент, и чинится это укорачиванием
    // команды, а не остановкой прогона.
    throw new Refusal(
      `строка журнала весит ${bytes} байт, предел ${MAX_LINE_BYTES}:`
      + ' запись потеряла бы атомарность',
    );
  }
  return line;
}

function parseLine(line) {
  try {
    return JSON.parse(line);
  } catch {
    return { type: 'parse-error' };
  }
}

function readEvents(file) {
  if (!existsSync(file)) return [];
  return readFileSync(file, 'utf-8')
    .split('\n')
    .filter(Boolean)
    .map(parseLine);
}

/**
 * Журнал одного этапа — один файл по ключу этапа. Каталог прогона складывают
 * из таких файлов; свёртка всего прогона — в `readRunState`.
 */
export class Journal {
  #dir; #file;

  constructor(dir, name) {
    this.#dir = dir;
    this.#file = join(dir, `${name}.jsonl`);
  }

  get dir() {
    return this.#dir;
  }

  append(event) {
    // Часы шагают и назад (синхронизация времени), а свёртка упорядочивает по `at`:
    // без этого `dupe` и `verdict` встали бы раньше своих находок и пропали.
    const at = Math.max(Date.now(), latestAt(this.#dir) + 1);
    appendFileSync(this.#file, `${encodeEvent({ ...event, at })}\n`, 'utf-8');
  }

  read() {
    return readEvents(this.#file);
  }

  /**
   * Состояние на этот момент. Перечитывается с диска каждый раз намеренно:
   * этап дописывает журнал, пока харнесс его читает.
   */
  state() {
    return JournalState.from(this.read());
  }

  /**
   * Процесс этапа мог умереть, не успев сказать ни слова. Без этой строки
   * «этап не отработал» неотличимо от «этап отработал и промолчал».
   */
  recordFailure({ stage, key, reason, rateLimit = null }) {
    this.append({
      type: 'stage-failed', stage, key, reason, rateLimit,
    });
  }
}

export class JournalState {
  #findings = [];
  #byId = new Map();
  #assessment = null;
  #summaries = new Map();
  #stopped = [];
  #resolved = [];
  #failures = [];
  #skippedLines = 0;

  static from(events) {
    const state = new JournalState();
    for (const event of events) state.apply(event);
    return state;
  }

  /**
   * Единственная дверь для записи. Редьюсеры — приватные методы этого класса:
   * знание «как событие меняет состояние» принадлежит состоянию, а не
   * командам харнесса, которые события производят.
   */
  apply(event) {
    this.#reducerFor(event.type)?.call(this, event);
    return this;
  }

  #reducerFor(type) {
    return {
      'finding': this.#recordFinding,
      // 'batch' не пишет собственный тип события: в журнал попадают события
      // вложенных команд ('finding', 'verdict', ...).
      'amend': this.#reviseFinding,
      'verdict': this.#recordVerdict,
      'dupe': this.#markDuplicate,
      'assess': this.#recordAssessment,
      'resolve': this.#recordResolve,
      'summary': this.#recordSummary,
      'stop': this.#recordStop,
      'stage-failed': this.#recordStageFailure,
      'parse-error': this.#recordSkippedLine,
    }[type];
  }

  get findings() {
    return [...this.#findings];
  }

  get survivingFindings() {
    return this.#findings.filter((finding) => finding.survives);
  }

  get keptFindings() {
    return this.#findings.filter((finding) => !finding.duplicateOf);
  }

  get failure() {
    return this.#failures.at(-1) ?? null;
  }

  get failures() {
    return [...this.#failures];
  }

  get assessment() {
    return this.#assessment;
  }

  /** Сводки разбирающих этапов: у каждого этапа своя, последняя запись побеждает. */
  get summaries() {
    return [...this.#summaries].map(([stage, text]) => ({ stage, text }));
  }

  get stopped() {
    return [...this.#stopped];
  }

  get resolved() {
    return [...this.#resolved];
  }

  get skippedLines() {
    return this.#skippedLines;
  }

  findingById(id) {
    return this.#byId.get(Number(id)) ?? null;
  }

  /** Состояние без находок этих этапов; номера остальных — сквозные по прогону, как были. */
  hideStages(stages) {
    this.#findings = this.#findings.filter((finding) => !stages.includes(finding.stage));
    this.#byId = new Map(this.#findings.map((finding) => [finding.id, finding]));
    return this;
  }

  #recordFinding(event) {
    const finding = new Finding({ ...event, id: this.#findings.length + 1 });
    this.#findings.push(finding);
    this.#byId.set(finding.id, finding);
  }

  #reviseFinding(event) {
    const {
      type, id, stage, at, ...fields
    } = event;
    this.findingById(id)?.revise(fields);
  }

  #recordVerdict(event) {
    this.findingById(event.id)?.applyVerdict(event);
  }

  #markDuplicate(event) {
    const finding = this.findingById(event.id);
    if (!finding || !this.#byId.has(Number(event.of)) || event.id === event.of) return;
    finding.markDuplicate(event.of, event.reason);
  }

  #recordAssessment(event) {
    this.#assessment = event;
  }

  #recordResolve(event) {
    if (this.#resolved.some((thread) => thread.id === event.id)) return;
    this.#resolved.push({ id: event.id, reason: event.reason, place: event.place ?? null });
  }

  #recordSummary(event) {
    this.#summaries.set(event.stage ?? '', event.text);
  }

  #recordStop(event) {
    this.#stopped.push({ stage: event.stage, name: event.name, reason: event.reason });
  }

  #recordStageFailure(event) {
    this.#failures.push(event);
  }

  #recordSkippedLine() {
    this.#skippedLines += 1;
  }
}

/**
 * Состояние прогона из уже созданных журналов, сложенных по времени записи.
 * Порядок по `at`, а не по имени файла: номер находки фиксируется на свёртке
 * и используется в `verdict --id`, сортировка по имени перенумеровала бы уже
 * выданные id.
 */
function foldEvents(events) {
  events.sort((a, b) => (a.at ?? 0) - (b.at ?? 0) || (a.stage ?? '').localeCompare(b.stage ?? ''));
  return JournalState.from(events);
}

export function foldJournals(journals) {
  return foldEvents(journals.flatMap((journal) => journal.read()));
}

function readDirEvents(dir) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((name) => name.endsWith('.jsonl'))
    .flatMap((name) => readEvents(join(dir, name)));
}

function latestAt(dir) {
  return readDirEvents(dir).reduce((latest, event) => (event.at > latest ? event.at : latest), 0);
}

export function readRunState(dir) {
  return foldEvents(readDirEvents(dir));
}
