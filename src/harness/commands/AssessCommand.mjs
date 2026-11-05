import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { LEVEL_COUNT } from '../../config.mjs';

const NATURES = ['mechanical', 'behavioral'];
const RISKS = ['low', 'medium', 'high'];

const LEVEL_HINT = `how deep the review goes: 1 (cheapest) to ${LEVEL_COUNT} (priciest)`;

export class AssessCommand extends Command {
  get name() {
    return 'assess';
  }

  get summary() {
    return 'record the verdict on the request: nature, risk and review depth';
  }

  get options() {
    return [
      {
        flags: '--nature <kind>', hint: 'does behaviour change', required: true, choices: NATURES,
      },
      {
        flags: '--risk <level>', hint: 'the same assessment in three steps', required: true, choices: RISKS,
      },
      { flags: '--level <number>', hint: LEVEL_HINT, required: true },
      { flags: '--reason <text>', hint: 'what justifies the verdicts', required: true },
    ];
  }

  build(values) {
    const level = Number(values.level);
    if (!Number.isInteger(level) || level < 1 || level > LEVEL_COUNT) {
      throw new Refusal(`--level — ${LEVEL_HINT}; пришло «${values.level}»`);
    }
    return {
      events: [{
        type: 'assess', nature: values.nature, risk: values.risk, reason: values.reason, level,
      }],
      confirmation: '',
    };
  }
}
