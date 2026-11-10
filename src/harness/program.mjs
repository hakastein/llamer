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

// Справка — в раскладке commander, как её печатал харнесс до переноса в LLaMer:
// этапы и тексты заданий выросли на ней.
const HELP_WIDTH = 80;
const MIN_WRAP = 40;

// Перенос по пробелам с сохранением переводов строк (commander Help.boxWrap).
function boxWrap(text, width) {
  return text.split('\n').map((line) => {
    const chunks = line.match(/\s*\S+/g);
    if (!chunks) return '';
    const lines = [chunks.shift()];
    for (const chunk of chunks) {
      if (lines.at(-1).length + chunk.length <= width) lines[lines.length - 1] += chunk;
      else lines.push(chunk.trimStart());
    }
    return lines.join('\n');
  }).join('\n');
}

function optionText(option) {
  const text = option.required ? `${option.hint} [required]` : option.hint;
  return option.choices ? `${text} (choices: ${option.choices.map((choice) => JSON.stringify(choice)).join(', ')})` : text;
}

export function helpOf(command) {
  const items = [...command.options.map((option) => [option.flags, optionText(option)]), ['-h, --help', 'display this message']];
  const termWidth = Math.max(...items.map(([term]) => term.length));
  const descriptionWidth = HELP_WIDTH - termWidth - 4;
  const item = ([term, text]) => {
    const wrapped = descriptionWidth < MIN_WRAP ? text : boxWrap(text, descriptionWidth);
    return `  ${term.padEnd(termWidth)}  ${wrapped.replace(/\n/g, `\n${' '.repeat(termWidth + 4)}`)}`;
  };
  const description = command.manual ? `${command.summary}\n\n${command.manual}` : command.summary;
  const usage = [HARNESS, command.name, '[options]', command.positionals].filter(Boolean).join(' ');
  return [`Usage: ${usage}`, '', boxWrap(description, HELP_WIDTH), '', 'Options:',
    ...items.map(item), ''].join('\n');
}

export function describeCommands(commands, names) {
  return commands
    .filter((command) => names.includes(command.name))
    .map((command) => `$ ${HARNESS} ${command.name} --help\n${helpOf(command)}`)
    .join('\n');
}

// Справки команд в задании этапа нет: команды, поля и форматы — только здесь.
function overview(commands, names) {
  const own = commands.filter((command) => names.includes(command.name));
  return [`${HARNESS} — record review events into the run journal.`, '', 'Stage commands:',
    ...own.map((command) => `  ${command.name}  ${command.summary}`),
    '', describeCommands(commands, names), `Command help: ${HARNESS} <command> --help`].join('\n');
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
      args: rest, options: parseOptionsOf(command), strict: true, allowPositionals: command.positionals !== null,
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
  if (command.positionals !== null) values.positionals = parsed.positionals;
  return { command, result: command.run(values, context) };
}
