import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { SEVERITIES, parseRelated } from '../../domain/Finding.mjs';

export class AmendCommand extends Command {
  get name() {
    return 'amend';
  }

  get summary() {
    return 'поправить уже записанную находку по её номеру';
  }

  get manual() {
    return 'Меняются только названные поля; номер остаётся, и вердикты и метки повтора'
      + ' продолжают указывать на неё. Правит только находки своего этапа.';
  }

  get options() {
    return [
      { flags: '--id <number>', hint: 'номер из ответа batch или list', required: true },
      { flags: '--text <text>', hint: 'что не так и как это решить' },
      {
        flags: '--severity <level>',
        hint: 'ступень, на которую находку ставит последствие',
        choices: SEVERITIES,
      },
      { flags: '--severity-reason <text>', hint: 'чем обоснован вес' },
      { flags: '--related <list>', hint: 'куда ты смотрел, «путь:строка» через запятую' },
      { flags: '--suggestion <code>', hint: 'полная замена выделенного фрагмента' },
    ];
  }

  build(values, context) {
    const id = Number(values.id);
    const finding = context.state.findingById(id);
    if (!finding) {
      throw new Refusal(`находки #${values.id} в журнале нет — сверь номера через list`);
    }
    if (finding.stage !== context.stage) {
      throw new Refusal(`находку #${id} записал этап «${finding.stage}», а не «${context.stage}»`);
    }

    const fields = {
      text: values.text,
      severity: values.severity,
      severityReason: values.severityReason,
      related: values.related === undefined ? undefined : parseRelated(values.related),
      suggestion: values.suggestion,
    };
    const named = Object.entries(fields).filter(([, value]) => value !== undefined);
    if (named.length === 0) throw new Refusal('назови хотя бы одно поле, которое меняешь');
    if (fields.suggestion !== undefined) {
      finding.assertSuggestionApplies(context.readFileLines(finding.file), fields.suggestion);
    }

    return {
      events: [{ type: 'amend', id, ...Object.fromEntries(named) }],
      confirmation: '',
    };
  }
}
