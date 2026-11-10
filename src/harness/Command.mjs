import { Refusal } from '../domain/Refusal.mjs';

export const BOOLEANS = ['true', 'false'];

export function longFlag(flags) {
  return flags.split(/[ ,|]/).find((part) => part.startsWith('--'));
}

export function optionKey(flags) {
  return longFlag(flags).replace(/^--/, '').replace(/-(\w)/g, (_, letter) => letter.toUpperCase());
}

/**
 * Команда харнесса описывает себя сама: имя, флаги с подсказками,
 * обязательность и допустимые значения. Из этого собираются и разбор
 * аргументов и `--help`.
 */
export class Command {
  get name() {
    throw new Error(`${this.constructor.name}: имя команды не объявлено`);
  }

  get summary() {
    return '';
  }

  get options() {
    return [];
  }

  get manual() {
    return '';
  }

  get prints() {
    return false;
  }

  /** Подсказка позиционных аргументов в Usage; null — команда их не берёт. */
  get positionals() {
    return null;
  }

  run(values, context) {
    this.assertValues(values);
    return this.build(values, context);
  }

  assertValues(values) {
    for (const option of this.options) {
      const flag = longFlag(option.flags);
      const value = values[optionKey(option.flags)];
      if (option.required && isEmpty(value)) {
        const allowed = option.choices ? ` (одно из: ${option.choices.join(', ')})` : '';
        throw new Refusal(`нужен ${flag}: ${option.hint}${allowed}`);
      }
      if (option.choices && (option.required || value !== undefined)) {
        this.assertOneOf(flag, option.choices, value);
      }
    }
  }

  assertOneOf(flag, allowed, value) {
    if (allowed.includes(value)) return;
    throw new Refusal(`${flag} — одно из: ${allowed.join(', ')}; пришло «${value ?? 'ничего'}»`);
  }

  build() {
    throw new Error(`${this.constructor.name}: build не реализован`);
  }
}

function isEmpty(value) {
  if (value === undefined || value === null || value === '') return true;
  return Array.isArray(value) && value.filter((item) => String(item).trim()).length === 0;
}
