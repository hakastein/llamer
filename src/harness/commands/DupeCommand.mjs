import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { HARNESS } from '../../agent/stageBin.mjs';

export class DupeCommand extends Command {
  get name() {
    return 'dupe';
  }

  get summary() {
    return 'mark a finding as a duplicate of another one';
  }

  get manual() {
    return 'Same defect as another finding: same place, same cause. The marked one drops'
      + ' out — --of should be the better-written of the two.';
  }

  get options() {
    return [
      { flags: '--id <number>', hint: 'the number of the duplicate finding', required: true },
      { flags: '--of <number>', hint: 'the number of the finding that stays', required: true },
      { flags: '--reason <text>', hint: 'why this is the same place and the same cause', required: true },
    ];
  }

  build(values, context) {
    const id = Number(values.id);
    const of = Number(values.of);
    const duplicate = this.find(context, id, '--id');
    const original = this.find(context, of, '--of');

    if (id === of) throw new Refusal('--id и --of совпадают: находка не может повторять саму себя');
    if (duplicate.duplicateOf) {
      throw new Refusal(`находка #${id} уже помечена повтором #${duplicate.duplicateOf}`);
    }
    if (original.duplicateOf) {
      throw new Refusal(`#${of} сама помечена повтором #${original.duplicateOf}:`
        + ' в --of ставится оставляемая находка, а не другой повтор'
        + ` — вероятно, это #${original.duplicateOf}`);
    }
    return {
      events: [{ type: 'dupe', id, of, reason: values.reason }],
      confirmation: '',
    };
  }

  find(context, id, flag) {
    const finding = context.state.findingById(id);
    if (!finding) throw new Refusal(`${flag}: находки с номером ${id} нет; список — ${HARNESS} list`);
    return finding;
  }
}
