import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';

export class ThreadCommand extends Command {
  get name() {
    return 'thread';
  }

  get summary() {
    return 'print a request thread in full, with the author\'s replies';
  }

  get prints() {
    return true;
  }

  get options() {
    return [{ flags: '--id <id>', hint: 'the thread id from the pack', required: true }];
  }

  build(values, context) {
    const thread = context.openThreads.find((candidate) => candidate.id === values.id);
    if (!thread) throw new Refusal(`треда ${values.id} нет среди открытых — id есть в задании`);
    const replies = (thread.full?.replies ?? []).map((reply) => `  ответ автора: ${reply}`);
    return { print: [`${thread.at} [${thread.id}]`, thread.full?.text ?? thread.text, ...replies].join('\n') };
  }
}
