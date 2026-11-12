import { pluralRu } from '../plural.mjs';
import { withRevisionMark } from '../gitlab/markdown.mjs';
import { buildRiskNote } from './risk-note.mjs';
import { buildSummaryComment } from './summary.mjs';
import { buildScopeBlock } from './scope.mjs';
import { collectShortfall } from './shortfall.mjs';
import { formatRunStats } from '../agent/cost.mjs';

export const STATUS_MARKER = '<!-- llamer-status -->';

/** Почему ревью пропущено: с `--since-last` пусто может быть и в дельте после прошлого прогона. */
export function nothingOwnText(task) {
  return task.since
    ? 'Новых своих изменений после прошлого прогона нет. Ревью пропущено.'
    : 'Разбирать нечего: голова не добавляет изменений к базе разбора. Ревью пропущено.';
}

/**
 * Изменения без своего кода — синк общих веток или задача, уже влитая в
 * целевую. Этапы не поднимаются, но молчание ревьюера читалось бы как сбой.
 */
export function nothingOwnNote({ task, markSha }) {
  const text = `_${nothingOwnText(task)}_`;
  return withRevisionMark(`${STATUS_MARKER}\n${text}\n\n${buildScopeBlock({ task })}`, markSha);
}

/**
 * Что прогон произвёл — данные, ни сети, ни печати. Оба доставщика читают
 * одно и то же, поэтому расходиться в текстах им нечем.
 */
export class ReviewReport {
  #journal; #materials; #stages; #markSha; #elapsedMs; #cached = null;

  constructor({
    journal, materials, stages, markSha, elapsedMs,
  }) {
    this.#journal = journal;
    this.#materials = materials;
    this.#stages = stages;
    this.#markSha = markSha;
    this.#elapsedMs = elapsedMs;
  }

  get assessment() { return this.#journal.assessment; }

  get riskNote() {
    if (!this.assessment) return null;
    return buildRiskNote({
      assessment: this.assessment,
      signals: this.#materials.signals,
      reviewed: buildScopeBlock({ task: this.#materials.task }),
    });
  }

  get resolve() { return this.#journal.resolved; }

  get summaries() { return this.#journal.summaries; }

  get requestDiff() { return this.#materials.requestDiff; }

  get taskAnchors() { return this.#materials.taskAnchors; }

  get baseSha() { return this.#materials.baseSha; }

  get markSha() { return this.#markSha; }

  get stages() { return this.#stages; }

  get elapsedMs() { return this.#elapsedMs; }

  // Дословный повтор не должен стать двумя тредами на одной строке: этапы пишут
  // независимо, а повторить запись агент может и сам, решив, что первая не
  // прошла. Разные замечания на одной строке остаются оба.
  get findings() { return this.#split().kept; }

  get repeats() { return this.#split().repeats; }

  #split() {
    if (this.#cached) return this.#cached;
    const seen = new Set();
    const kept = [];
    const repeats = [];
    for (const finding of this.#journal.survivingFindings) {
      if (seen.has(finding.key)) {
        repeats.push(`находка ${finding.at} уже есть в этом прогоне — дословный повтор снят`);
        continue;
      }
      seen.add(finding.key);
      kept.push(finding);
    }
    this.#cached = { kept, repeats };
    return this.#cached;
  }

  shortfall(unpublished) {
    return collectShortfall({ state: this.#journal, unpublished, records: this.#stages });
  }

  // Пустая сводка не должна стать пустым комментарием в MR — доставщик решает
  // по этому значению, звать ли `postNote` вовсе.
  summary(unpublished) {
    return buildSummaryComment({ summaries: this.summaries, gaps: this.shortfall(unpublished).author });
  }

  status(unpublished) {
    const label = pluralRu(unpublished.length, 'замечание', 'замечания', 'замечаний');
    const status = unpublished.length > 0
      ? `_Ревью выполнено, ${unpublished.length} ${label} опубликовать не удалось — см. сводку._`
      : '_Ревью выполнено._';
    const stats = formatRunStats(this.#stages, { elapsedMs: this.#elapsedMs });
    // Метка статуса — точка отсчёта `--since-last`: с сорванным или прекращённым этапом
    // или неопубликованными находками она дала бы следующему прогону пропустить неразобранное.
    const complete = unpublished.length === 0 && this.shortfall(unpublished).failed === 0
      && this.#journal.stopped.length === 0;
    return withRevisionMark(`${STATUS_MARKER}\n${status}\n\n${stats}`, complete ? this.#markSha : null);
  }
}
