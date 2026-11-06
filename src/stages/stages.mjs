import { ReviewStage, FINDING_COMMANDS } from './ReviewStage.mjs';
import {
  bullets, formatFindings, formatStages, packBlock,
} from './prompt.mjs';
import { formatSignalsSummary } from '../materials/signals.mjs';
import { commentsContext, findingContexts } from '../materials/windows.mjs';
import { analysisStages, BUILTIN_STAGES } from '../config.mjs';

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
          + (thread.reply ? `\n  author's reply: ${thread.reply}` : ''))
        .join('\n'),
      PACK: materials.renderPack(),
    };
  }
}

/**
 * Чем встроенные разбирающие этапы отличаются друг от друга, кроме шаблона:
 * вывод по реквесту (`summary`) пишет только бизнес-логика, память проекта
 * нужна логике и качеству, задача из трекера качеству кода не нужна. Свой
 * этап конфига устроен как бизнес-логика.
 */
const ANALYSIS_TRAITS = {
  logic: { summary: true, projectMemory: true, withTask: true },
  quality: { summary: false, projectMemory: true, withTask: false },
  comments: { summary: false, projectMemory: false, withTask: true },
  tests: { summary: false, projectMemory: false, withTask: true },
};
const CUSTOM_TRAITS = ANALYSIS_TRAITS.logic;

/**
 * Разбирающий этап — встроенный или свой из конфига: скиллы, маски путей,
 * уровень и расширение `allow`. У встроенного свой шаблон, у своего — общий
 * `analysis` с задачей из конфига.
 */
export class AnalysisStage extends ReviewStage {
  #settings;

  constructor(toolkit, settings, driver) {
    super(toolkit, driver);
    this.#settings = settings;
  }

  get settings() { return this.#settings; }

  get key() { return this.settings.key; }

  get #builtin() { return this.key in BUILTIN_STAGES; }

  get #traits() { return ANALYSIS_TRAITS[this.key] ?? CUSTOM_TRAITS; }

  get templateKey() { return this.#builtin ? this.key : 'analysis'; }

  get projectMemory() { return this.#traits.projectMemory; }

  // Комментарий сам и есть место находки: о декларации этап скажет в --related, когда она есть.
  get relatedRequired() { return this.key !== 'comments'; }

  get commands() {
    return [...FINDING_COMMANDS, ...(this.#traits.summary ? ['summary'] : []), ...super.commands];
  }

  applies(materials) {
    return materials.filesMatching(this.settings.paths).length > 0;
  }

  skipReason() {
    return `в изменениях нет файлов под маски ${this.settings.paths.join(', ')}`;
  }

  values({ materials }) {
    const wanted = this.template.names;
    const values = {};
    if (wanted.has('FILES')) values.FILES = bullets(materials.filesMatching(this.settings.paths));
    if (wanted.has('PACK')) values.PACK = materials.renderPack({ withTask: this.#traits.withTask });
    if (wanted.has('COMMENTS')) {
      values.COMMENTS = packBlock(commentsContext(materials.diff, materials.cwd), '(no added comments)');
    }
    if (wanted.has('TASK')) values.TASK = this.settings.task.trim();
    if (wanted.has('TASK_NOTE')) {
      values.TASK_NOTE = materials.issueId
        ? `Task ${materials.issueId} is in the pack below — check the implementation `
          + 'against what was asked for.'
        : '';
    }
    return values;
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
