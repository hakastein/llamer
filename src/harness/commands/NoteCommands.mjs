import { Command } from '../Command.mjs';

export class SummaryCommand extends Command {
  get name() {
    return 'summary';
  }

  get summary() {
    return 'close the review with the verdict on the whole request';
  }

  get options() {
    return [{ flags: '--text <text>', hint: 'the verdict on the request as a whole', required: true }];
  }

  build(values) {
    return { events: [{ type: 'summary', text: values.text }], confirmation: '' };
  }
}

export class StopCommand extends Command {
  get name() {
    return 'stop';
  }

  get summary() {
    return 'abort the stage and say why';
  }

  get options() {
    return [{ flags: '--reason <text>', hint: 'why the review was aborted', required: true }];
  }

  build(values, context) {
    return {
      events: [{
        type: 'stop', stage: context.stage, name: context.name, reason: values.reason,
      }],
      confirmation: '',
    };
  }
}
