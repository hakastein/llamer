import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { Finding, SEVERITIES, parseRelated } from '../../domain/Finding.mjs';

export class FindingCommand extends Command {
  get name() {
    return 'finding';
  }

  get summary() {
    return 'записать замечание к реквесту, привязанное к строкам диффа';
  }

  get manual() {
    return 'Выдели всё, о чём замечание, — условие, вызов, ветку или метод целиком: туда'
      + ' встанет тред.\n'
      + 'Для УДАЛЁННОГО кода — --deleted, привязка к старой стороне; иначе тред встанет на'
      + ' новую сторону, на то, что пришло на замену.';
  }

  get options() {
    return [
      { flags: '--file <path...>', hint: 'путь от корня репозитория, как в диффе', required: true },
      { flags: '--line <number>', hint: 'первая строка фрагмента', required: true },
      {
        flags: '--line-end <number>',
        hint: 'последняя его строка; умолчания нет, для однострочного замечания повтори --line',
        required: true,
      },
      { flags: '--text <text>', hint: 'что не так и как это решить', required: true },
      {
        flags: '--severity <level>', hint: 'ступень, на которую находку ставит последствие', required: true, choices: SEVERITIES,
      },
      {
        flags: '--severity-reason <text>',
        hint: 'то одно последствие, что ставит её туда, — не то же, что --text',
        required: true,
      },
      {
        flags: '--related <list>',
        hint: 'на чём стоит замечание: «путь», «путь:строка» или «путь:строка-строка»'
          + ' через запятую; остальное в значении молча отбрасывается. Строки — из новой'
          + ' версии даже с --deleted',
      },
      { flags: '--trace <text>', hint: 'цепочка рассуждения для находки о взаимодействии' },
      {
        flags: '--suggestion <code>',
        hint: 'заменяет ВЕСЬ фрагмент, любой длины; кнопка применяет его не читая, поэтому'
          + ' предлагай, только когда знаешь правку целиком. Разметку добавят за тебя',
      },
      {
        flags: '--deleted',
        hint: 'фрагмент — удалённые строки: --line/--line-end из версии ДО реквеста'
          + ' (git show <база>:<файл>)',
      },
    ];
  }

  build(values, context) {
    const related = parseRelated(values.related);
    if (related.length === 0) {
      throw new Refusal('нужен --related: куда ты смотрел, устанавливая находку'
        + ' («путь:строка» через запятую). Если находка очевидна по одной строке,'
        + ' назови эту строку.');
    }

    const finding = new Finding({
      file: Array.isArray(values.file) ? values.file[0] : values.file,
      line: values.line,
      lineEnd: values.lineEnd,
      deleted: Boolean(values.deleted),
      text: values.text,
      severity: values.severity,
      severityReason: values.severityReason,
      suggestion: values.suggestion ?? null,
      related,
      trace: values.trace ?? null,
    });
    finding.assertAnchored(context.diff);
    if (finding.suggestion !== null) {
      finding.assertSuggestionApplies(context.readFileLines(finding.file), finding.suggestion);
    }
    const event = finding.toEvent();
    return {
      events: [event],
      confirmation: ` ${event.file}:${event.line}-${event.lineEnd}`,
    };
  }
}
