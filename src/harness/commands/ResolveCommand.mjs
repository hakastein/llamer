import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';

export class ResolveCommand extends Command {
  get name() {
    return 'resolve';
  }

  get summary() {
    return 'закрыть наш открытый тред, который текущий код снял';
  }

  get manual() {
    return 'Закрывает наш тред, оставленный открытым прошлыми прогонами. Читай замечание как'
      + ' ТРЕБОВАНИЕ: оно снято, когда текущий код ему отвечает, где бы этот код теперь ни жил.';
  }

  get options() {
    return [
      { flags: '--id <id>', hint: 'id треда из списка в твоём задании', required: true },
      { flags: '--reason <text>', hint: 'что именно в этой ревизии сняло замечание', required: true },
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
