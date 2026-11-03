import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { LEVEL_COUNT } from '../../config.mjs';

const NATURES = ['mechanical', 'behavioral'];
const RISKS = ['low', 'medium', 'high'];

const LEVEL_HINT = `глубина разбора: от 1 (дешевле всего, поверхностно) до ${LEVEL_COUNT}`
  + ' (дороже всего, глубже всего); каждая ступень — модель × усилие драйвера';

export class AssessCommand extends Command {
  get name() {
    return 'assess';
  }

  get summary() {
    return 'записать вердикт по реквесту: природа правки, риск и глубина разбора';
  }

  get options() {
    return [
      {
        flags: '--nature <kind>', hint: 'меняется ли поведение', required: true, choices: NATURES,
      },
      {
        flags: '--risk <level>', hint: 'та же оценка в три ступени', required: true, choices: RISKS,
      },
      { flags: '--level <number>', hint: LEVEL_HINT, required: true },
      { flags: '--reason <text>', hint: 'чем обоснованы вердикты', required: true },
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
