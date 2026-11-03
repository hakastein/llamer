import { Command, optionKey } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';

const WINDOW = 40;

// Сообщение парсера называет позицию числом; агент ищет её счётом символов и
// тратит на это ход.
function around(raw, message) {
  const at = /position (\d+)/.exec(message);
  if (!at) return '';
  const position = Number(at[1]);
  const from = Math.max(0, position - WINDOW);
  return `\nЗдесь: …${raw.slice(from, position)}<-- ЗДЕСЬ -->${raw.slice(position, position + WINDOW)}…`;
}

const VERDICT_STATUS = { true: 'опровергнута', false: 'в силе' };

// Разбор аргументов даёт значения строками и разворачивает `<name...>` в
// массив; батч подаёт JSON напрямую, так что то же самое делается здесь —
// иначе JSON-булев `false` и одиночная строка вместо массива уходят в команду
// в чужой для неё форме.
function normalized(command, values) {
  const result = { ...values };
  for (const option of command.options) {
    const key = optionKey(option.flags);
    if (result[key] === undefined) continue;
    if (option.flags.includes('...') && !Array.isArray(result[key])) result[key] = [result[key]];
    // Строкой становится значение объявленной опции (`--refuted <bool>`), а
    // флаг без значения (`--deleted`) остаётся булевым: `Boolean('false')`
    // истинно, и находка уехала бы к удалённым строкам с `"deleted": false`.
    else if (typeof result[key] !== 'string' && option.flags.includes('<')) {
      result[key] = String(result[key]);
    }
  }
  return result;
}

function place(event) {
  if (!event.file) return '';
  if (event.line === undefined) return ` ${event.file}`;
  const range = event.lineEnd && event.lineEnd !== event.line ? `-${event.lineEnd}` : '';
  return ` ${event.file}:${event.line}${range}`;
}

function detailOf(event) {
  if (event.type === 'verdict') return ` ${VERDICT_STATUS[String(Boolean(event.refuted))]}`;
  if (event.type === 'dupe') return ` повтор #${event.of}`;
  return '';
}

function describe(event) {
  const weight = event.severity ? ` ${event.severity}` : '';
  const id = event.id === undefined ? '' : ` #${event.id}`;
  const text = event.text ?? event.reason ?? '';
  const quoted = text ? ` «${text.replace(/\s+/g, ' ').slice(0, 100)}»` : '';
  return `${event.type}${id}${detailOf(event)}${place(event)}${weight}${quoted}`;
}

export class BatchCommand extends Command {
  // Диспетчер по имени команды приходит конструктором: createCommands()
  // наполняет замыкание по ходу сборки списка.
  constructor(commandByName) {
    super();
    this.commandByName = commandByName;
  }

  get name() {
    return 'batch';
  }

  get summary() {
    return 'записать несколько событий журнала одним вызовом';
  }

  get manual() {
    return 'Типы в одном массиве можно смешивать. Отбитое событие не записывается и'
      + ' возвращается предупреждением с его номером — повтори его исправленным,'
      + ' остальные уже записаны. Весь вызов отбивает только неразборчивый JSON.'
      + ' Номера находок для `amend` и `dupe` бери из `list`: этапы пишут параллельно,'
      + ' и порядок складывается в журнале, а не в этом ответе.';
  }

  get options() {
    return [
      {
        flags: '--json <array>',
        hint: 'JSON-массив событий: {"type":"finding",…}, поля названы как флаги'
          + ' одиночной команды в camelCase',
        required: true,
      },
    ];
  }

  parse(raw) {
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch (error) {
      throw new Refusal(`--json — не JSON (${error.message}), ничего не записано.`
        + `${around(raw, error.message)}`);
    }
    const events = Array.isArray(parsed) ? parsed : [parsed];
    if (events.length === 0) throw new Refusal('в --json нет ни одного события');
    return events;
  }

  runAll(events, context) {
    const records = [];
    const warnings = [];

    events.forEach((event, index) => {
      if (event === null || typeof event !== 'object' || Array.isArray(event)) {
        warnings.push({ index, message: `элемент — не объект события (${JSON.stringify(event)}), не записан` });
        return;
      }
      const { type, ...values } = event;
      const command = context.commands.includes(type) ? this.commandByName(type) : null;
      if (!command) {
        warnings.push({
          index,
          message: `типа «${type}» среди команд этапа нет (${context.commands.join(', ')}) — не записано`,
        });
        return;
      }
      if (command.prints) {
        warnings.push({ index, message: `команда ${type} печатает ответ и ничего не пишет — вызови её отдельно` });
        return;
      }
      try {
        const built = command.run(normalized(command, values), context);
        records.push({ events: built.events, line: describe({ ...event, ...built.events[0] }) });
      } catch (error) {
        // Отбитое не записывается: позицию для GitLab по такой строке не собрать.
        if (!(error instanceof Refusal)) throw error;
        warnings.push({ index, message: `${error.message} — не записано, повтори исправленным` });
      }
    });
    return { records, warnings };
  }

  build(values, context) {
    const events = this.parse(values.json);
    const { records, warnings } = this.runAll(events, context);
    const lines = records.map((record) => record.line);
    if (warnings.length > 0) {
      lines.push('предупреждения:');
      for (const warning of warnings) lines.push(`  элемент ${warning.index + 1}: ${warning.message}`);
    }
    return {
      events: records.flatMap((record) => record.events),
      confirmation: `\n${lines.join('\n')}`,
    };
  }
}
