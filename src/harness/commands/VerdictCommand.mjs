import { Command, BOOLEANS } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { HARNESS } from '../../agent/stageBin.mjs';
import { SEVERITIES } from '../../domain/Finding.mjs';

export class VerdictCommand extends Command {
  get name() {
    return 'verdict';
  }

  get summary() {
    return 'pass a verdict on a recorded finding: stands or refuted';
  }

  get options() {
    return [
      { flags: '--id <number>', hint: 'the #N of the finding from the list in your task', required: true },
      {
        flags: '--refuted <bool>', hint: 'does the finding fall', required: true, choices: BOOLEANS,
      },
      {
        flags: '--reason <text>',
        hint: 'the decisive fact with file and line: what refutes it, or why it stands'
          + ' and which step of the scale you confirmed',
        required: true,
      },
      {
        flags: '--severity <level>',
        hint: 'the revised severity; goes only together with --severity-reason',
        choices: SEVERITIES,
      },
      { flags: '--severity-reason <text>', hint: 'what the revised severity rests on' },
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
