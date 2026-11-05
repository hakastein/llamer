import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { SEVERITIES, parseRelated } from '../../domain/Finding.mjs';

export class AmendCommand extends Command {
  get name() {
    return 'amend';
  }

  get summary() {
    return 'correct a finding you already recorded, by its number';
  }

  get manual() {
    return 'Only the named fields change; the number stays, so verdicts and duplicate'
      + ' marks keep pointing at it. Amends only findings recorded by your own stage.';
  }

  get options() {
    return [
      { flags: '--id <number>', hint: 'the number the batch report printed', required: true },
      { flags: '--text <text>', hint: 'what is wrong and how to solve it' },
      {
        flags: '--severity <level>',
        hint: 'the level the consequence puts the finding on',
        choices: SEVERITIES,
      },
      { flags: '--severity-reason <text>', hint: 'what justifies the severity' },
      { flags: '--related <list>', hint: 'where you looked, «path:line» comma-separated' },
      { flags: '--suggestion <code>', hint: 'a complete replacement for the highlighted fragment' },
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
