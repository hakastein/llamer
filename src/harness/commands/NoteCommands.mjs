import { Command } from '../Command.mjs';

export class SummaryCommand extends Command {
  get name() {
    return 'summary';
  }

  get summary() {
    return 'закрыть разбор выводом по реквесту в рамках своего мандата';
  }

  get options() {
    return [{ flags: '--text <text>', hint: 'вывод по реквесту целиком', required: true }];
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
    return 'прекратить этап и сказать почему';
  }

  get options() {
    return [{ flags: '--reason <text>', hint: 'почему разбор прекращён', required: true }];
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
