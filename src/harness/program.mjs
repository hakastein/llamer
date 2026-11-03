import { parseArgs } from 'node:util';
import { Refusal } from '../domain/Refusal.mjs';
import { HARNESS } from '../agent/stageBin.mjs';
import { longFlag, optionKey } from './Command.mjs';

const PARSE_ERRORS = {
  ERR_PARSE_ARGS_UNKNOWN_OPTION: 'неизвестный флаг',
  ERR_PARSE_ARGS_INVALID_OPTION_VALUE: 'флаг без значения или значение не того вида',
  ERR_PARSE_ARGS_UNEXPECTED_POSITIONAL: 'лишний аргумент без флага',
};

function parseOptionsOf(command) {
  const options = { help: { type: 'boolean', short: 'h' } };
  for (const option of command.options) {
    options[longFlag(option.flags).slice(2)] = {
      type: option.flags.includes('<') ? 'string' : 'boolean',
      multiple: option.flags.includes('...'),
    };
  }
  return options;
}

export function helpOf(command) {
  const lines = [`Использование: ${HARNESS} ${command.name} [флаги]`, '', command.summary];
  if (command.manual) lines.push('', command.manual);
  if (command.options.length > 0) {
    lines.push('', 'Флаги:');
    for (const option of command.options) {
      const marks = [
        option.required ? 'обязателен' : null,
        option.choices ? `одно из: ${option.choices.join(', ')}` : null,
      ].filter(Boolean);
      lines.push(`  ${option.flags}  ${option.hint}${marks.length > 0 ? ` [${marks.join('; ')}]` : ''}`);
    }
  }
  return `${lines.join('\n')}\n`;
}

export function describeCommands(commands, names) {
  return commands
    .filter((command) => names.includes(command.name))
    .map((command) => `$ ${HARNESS} ${command.name} --help\n${helpOf(command)}`)
    .join('\n');
}

function overview(commands, names) {
  const own = commands.filter((command) => names.includes(command.name));
  return [`${HARNESS} — запись событий ревью в журнал прогона.`, '', 'Команды этапа:',
    ...own.map((command) => `  ${command.name}  ${command.summary}`),
    '', `Справка по команде: ${HARNESS} <команда> --help`].join('\n');
}

/**
 * Разбор вызова и диспетчеризация. Команда вне списка этапа отбивается здесь,
 * до исполнения: разрешения песочницы драйвера только объявляют команды, а
 * держит набор команд этапа харнесс.
 *
 * @returns {{ print: string } | { command: object, result: object }}
 */
export function runHarness({ argv, commands, context }) {
  const [name, ...rest] = argv;
  if (!name || name === '--help' || name === '-h') return { print: overview(commands, context.commands) };
  const command = commands.find((candidate) => candidate.name === name);
  if (!command || !context.commands.includes(name)) {
    throw new Refusal(`команды «${name}» у этапа «${context.stage}» нет; его команды: ${context.commands.join(', ')}`);
  }

  let parsed;
  try {
    parsed = parseArgs({
      args: rest, options: parseOptionsOf(command), strict: true, allowPositionals: false,
    });
  } catch (error) {
    throw new Refusal(`${PARSE_ERRORS[error.code] ?? 'аргументы не разобраны'}: ${error.message}`);
  }
  if (parsed.values.help) return { print: helpOf(command) };

  const values = {};
  for (const option of command.options) {
    const value = parsed.values[longFlag(option.flags).slice(2)];
    if (value !== undefined) values[optionKey(option.flags)] = value;
  }
  return { command, result: command.run(values, context) };
}
