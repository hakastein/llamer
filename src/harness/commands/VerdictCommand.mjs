import { Command, BOOLEANS } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { HARNESS } from '../../agent/stageBin.mjs';
import { SEVERITIES } from '../../domain/Finding.mjs';

export class VerdictCommand extends Command {
  get name() {
    return 'verdict';
  }

  get summary() {
    return 'вынести вердикт по записанной находке: в силе или опровергнута';
  }

  get options() {
    return [
      { flags: '--id <number>', hint: '#N находки из списка в твоём задании', required: true },
      {
        flags: '--refuted <bool>', hint: 'падает ли находка', required: true, choices: BOOLEANS,
      },
      {
        flags: '--reason <text>',
        hint: 'решающий факт с файлом и строкой: что её опровергает или почему она стоит'
          + ' и какую ступень веса ты подтвердил',
        required: true,
      },
      {
        flags: '--severity <level>',
        hint: 'пересмотренный вес; идёт только вместе с --severity-reason',
        choices: SEVERITIES,
      },
      { flags: '--severity-reason <text>', hint: 'на чём стоит пересмотренный вес' },
    ];
  }

  build(values, context) {
    const id = Number(values.id);
    const target = context.state.findingById(id);
    if (!target) throw new Refusal(`находки с номером ${id} нет; список — ${HARNESS} list`);
    if (target.duplicateOf) {
      throw new Refusal(`находка #${id} помечена повтором #${target.duplicateOf}`
        + ' и до реквеста не дойдёт: вердикт ей не нужен.'
        + ' Суди находки из списка в своём задании');
    }
    if (values.severity && !values.severityReason) {
      throw new Refusal('вместе с --severity нужен --severity-reason');
    }
    return {
      events: [{
        type: 'verdict',
        id,
        refuted: values.refuted === 'true',
        reason: values.reason,
        severity: values.severity ?? null,
        severityReason: values.severityReason ?? null,
      }],
      confirmation: '',
    };
  }
}
