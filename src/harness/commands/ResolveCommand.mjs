import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';

export class ResolveCommand extends Command {
  get name() {
    return 'resolve';
  }

  get summary() {
    return 'close an open review thread the current code has settled';
  }

  get manual() {
    return 'Closes a thread of ours that earlier runs left open. Treat the remark as a'
      + ' REQUIREMENT: it is settled once the current code satisfies it, wherever that'
      + ' code now lives.';
  }

  get options() {
    return [
      { flags: '--id <id>', hint: 'the thread id from the list in your task', required: true },
      { flags: '--reason <text>', hint: 'what exactly settled the remark in this revision', required: true },
    ];
  }

  build(values, context) {
    const { openThreads } = context;
    const thread = openThreads.find((candidate) => candidate.id === values.id);
    if (!thread) {
      throw new Refusal(`треда «${values.id}» среди открытых тредов прогона нет.`
        + ` Открытые:\n${openThreads.map((item) => `${item.id} — ${item.at}`).join('\n')}`);
    }
    return {
      events: [{
        type: 'resolve', id: thread.id, place: thread.at, reason: values.reason,
      }],
      confirmation: ` ${thread.at}`,
    };
  }
}
