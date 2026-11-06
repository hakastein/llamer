import { Refusal } from './Refusal.mjs';

export const SEVERITIES = ['minor', 'major', 'critical'];

export const MAX_SUGGESTION_SPAN = 20;

export function parseRelated(raw) {
  if (!raw) return [];
  return raw.split(',')
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => {
      const parsed = /^([^:]+)(?::(\d+)(?:-(\d+))?)?$/.exec(item);
      if (!parsed) return null;
      const line = parsed[2] ? Number(parsed[2]) : null;
      return {
        file: parsed[1].trim(),
        line,
        lineEnd: parsed[3] ? Number(parsed[3]) : line,
      };
    })
    .filter((related) => related
      && !related.file.includes('..')
      && !related.file.startsWith('/'));
}

export class Finding {
  constructor({
    id = null, file, line, lineEnd, deleted = false, text,
    severity, severityReason = null, suggestion = null, related = [], trace = null,
    stage = null, driver = null,
  }) {
    this.id = id;
    this.file = file;
    this.line = Number(line);
    this.lineEnd = Number(lineEnd);
    this.deleted = Boolean(deleted);
    this.text = text;
    this.severity = severity;
    this.severityReason = severityReason;
    this.suggestion = suggestion;
    this.related = Array.isArray(related) ? related : [];
    this.trace = trace;
    this.stage = stage;
    this.driver = driver;

    // `refuted: false` одинаков у проверенной находки и у той, до которой
    // скептик не дошёл: эти два случая разводит `verdicted`.
    this.verdicted = false;
    this.refuted = false;
    this.refuteReason = null;
    // Помеченный повтор не публикуется, но из журнала не пропадает: иначе
    // перекос дедупликатора в сторону лишней резки не заметить.
    this.duplicateOf = null;
    this.duplicateReason = null;
  }

  get at() {
    return `${this.file}:${this.line}-${this.lineEnd}`;
  }

  get key() {
    return `${this.at}\n${this.text}`;
  }

  get span() {
    return this.lineEnd - this.line + 1;
  }

  get survives() {
    return !this.refuted && !this.duplicateOf;
  }

  get awaitsVerdict() {
    return !this.verdicted && !this.duplicateOf;
  }

  revise({
    text, severity, severityReason, suggestion, related,
  } = {}) {
    if (text != null) this.text = text;
    if (severity != null) this.severity = severity;
    if (severityReason != null) this.severityReason = severityReason;
    if (suggestion != null) this.suggestion = suggestion;
    if (related != null) this.related = related;
  }

  markDuplicate(of, reason) {
    this.duplicateOf = of;
    this.duplicateReason = reason ?? null;
  }

  applyVerdict({ refuted, reason, severity = null, severityReason = null }) {
    this.verdicted = true;
    this.refuted = Boolean(refuted);
    this.refuteReason = reason ?? null;
    if (severity) {
      this.severity = severity;
      this.severityReason = severityReason ?? this.severityReason;
    }
  }

  assertAnchored(diff) {
    const { deleted } = this;
    if (!diff.touches(this.file, { deleted })) {
      throw new Refusal(deleted
        ? `в файле ${this.file} эти изменения строк не удаляли — --deleted не к чему привязать`
        : `файла ${this.file} нет среди изменений задачи`);
    }
    for (const number of [this.line, this.lineEnd]) {
      if (diff.hasLine(this.file, number, { deleted })) continue;
      throw new Refusal(
        `строка ${number} в ${this.file} ${deleted ? 'не удалена' : 'не изменена'} этими изменениями`
        + ` — тред к ней не привязать. ${deleted ? 'Удалённые' : 'Изменённые'} строки:`
        + ` ${diff.linesOf(this.file, { deleted }).join(', ')}`,
      );
    }
    if (this.lineEnd < this.line) throw new Refusal('--line-end меньше --line');
  }

  assertSuggestionApplies(fileLines, suggestion) {
    // Правка кнопкой заменяет строки новой версии; у удалённой строки их нет.
    if (this.deleted) {
      throw new Refusal(
        '--suggestion не привязать к удалённой строке: заменять нечего,'
        + ' опиши правку в --text',
      );
    }
    if (this.span > MAX_SUGGESTION_SPAN) {
      throw new Refusal(
        `правка на ${this.span} строк слишком велика: предел ${MAX_SUGGESTION_SPAN}.`
        + ' Правку такого размера объясняют текстом, а не кнопкой применения',
      );
    }
    if (this.lineEnd > fileLines.length) {
      throw new Refusal(
        `строка ${this.lineEnd} за концом ${this.file} (в нём ${fileLines.length} строк)`,
      );
    }
    if (fileLines.slice(this.line - 1, this.lineEnd).join('\n') === suggestion) {
      throw new Refusal(
        'правка совпадает с текущим кодом — заменять нечего.'
        + ' Проверь --line и --line-end',
      );
    }
  }

  toEvent() {
    return {
      type: 'finding',
      file: this.file,
      line: this.line,
      lineEnd: this.lineEnd,
      deleted: this.deleted,
      text: this.text,
      severity: this.severity,
      severityReason: this.severityReason,
      suggestion: this.suggestion,
      related: this.related,
      trace: this.trace,
    };
  }

  /** Находка в JSON-отчёте: то, что читает запустивший прогон агент. */
  toJSON() {
    return {
      id: this.id,
      stage: this.stage,
      driver: this.driver,
      file: this.file,
      line: this.line,
      lineEnd: this.lineEnd,
      deleted: this.deleted,
      severity: this.severity,
      severityReason: this.severityReason,
      text: this.text,
      suggestion: this.suggestion,
      related: this.related,
      trace: this.trace,
    };
  }
}
