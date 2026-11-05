import { ReviewStage, FINDING_COMMANDS } from './ReviewStage.mjs';
import { bullets, formatFindings, formatStages } from './prompt.mjs';
import { formatSignalsSummary } from '../materials/signals.mjs';
import { findingContexts } from '../materials/windows.mjs';
import { analysisStages } from '../config.mjs';

/** Оценщик: природа правки, риск и уровень, на котором пойдёт разбор. */
export class AssessStage extends ReviewStage {
  get key() { return 'assess'; }

  get name() { return 'оценщик'; }

  get commands() { return ['assess', ...super.commands]; }

  values({ materials }) {
    return {
      SIGNALS: formatSignalsSummary(materials.signals),
      FILES: bullets(materials.files),
      PACK: materials.renderPack(),
    };
  }

  assessment() {
    return this.state().assessment;
  }
}

/** Треды: сверяет наши открытые треды прошлых прогонов с текущим кодом. */
export class ThreadsStage extends ReviewStage {
  get key() { return 'threads'; }

  get name() { return 'треды'; }

  get commands() { return ['resolve', 'thread', 'list', 'batch', 'blame', ...super.commands]; }

  applies(materials) {
    return materials.openThreads.length > 0;
  }

  skipReason() {
    return 'наших открытых тредов нет';
  }

  brief(materials) {
    return { ...super.brief(materials), threads: materials.openThreads };
  }

  values({ materials }) {
    return {
      THREADS: materials.openThreads
        .map((thread) => `- id ${thread.id} — ${thread.at}\n  ${thread.text}`
          + (thread.reply ? `\n  ответ автора: ${thread.reply}` : ''))
        .join('\n'),
      PACK: materials.renderPack(),
    };
  }
}

/**
 * Разбирающий этап — встроенный или свой из конфига: мандат, скиллы, маски
 * путей, уровень и расширение `allow`. Пишет находки и сводку по своему мандату.
 */
export class AnalysisStage extends ReviewStage {
  #settings;

  constructor(toolkit, settings) {
    super(toolkit);
    this.#settings = settings;
  }

  get settings() { return this.#settings; }

  get key() { return this.settings.key; }

  get templateKey() { return 'analysis'; }

  get projectMemory() { return true; }

  get commands() { return [...FINDING_COMMANDS, 'summary', ...super.commands]; }

  applies(materials) {
    return materials.filesMatching(this.settings.paths).length > 0;
  }

  skipReason() {
    return `в изменениях нет файлов под маски ${this.settings.paths.join(', ')}`;
  }

  values({ materials }) {
    return {
      NAME: this.key,
      MANDATE: this.settings.mandate.trim(),
      FILES: bullets(materials.filesMatching(this.settings.paths)),
      TASK_NOTE: materials.issueId
        ? `Задача ${materials.issueId} — в пакете ниже: сверь реализацию с тем, что просили.`
        : '',
      PACK: materials.renderPack(),
    };
  }
}

/** Дедупликация: помечает находки разных этапов про одно место и одну причину. */
export class DedupeStage extends ReviewStage {
  get key() { return 'dedupe'; }

  get name() { return 'дедупликация'; }

  get commands() { return ['dupe', 'list', 'batch', 'blame', ...super.commands]; }

  applies(materials, journal) {
    return journal.findings.length > 1;
  }

  skipReason() {
    return 'находок меньше двух — дублировать нечему';
  }

  values({ journal }) {
    return {
      STAGES: formatStages(analysisStages(this.toolkit.config)),
      FINDINGS: formatFindings(journal.findings),
    };
  }
}

/** Скептик: вердикт по каждой уцелевшей находке, вес в обе стороны. */
export class SkepticStage extends ReviewStage {
  get key() { return 'skeptic'; }

  get name() { return 'скептик'; }

  get projectMemory() { return true; }

  get commands() { return ['verdict', 'list', 'blame', 'batch', ...super.commands]; }

  applies(materials, journal) {
    return journal.keptFindings.length > 0;
  }

  skipReason() {
    return 'после дедупликации находок не осталось';
  }

  values({ materials, journal }) {
    return {
      STAGES: formatStages(analysisStages(this.toolkit.config)),
      FINDINGS: formatFindings(findingContexts(journal.keptFindings, materials.cwd)),
      PACK: materials.renderPack(),
    };
  }
}
