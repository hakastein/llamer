import { AssessCommand } from './commands/AssessCommand.mjs';
import { BatchCommand } from './commands/BatchCommand.mjs';
import { FindingCommand } from './commands/FindingCommand.mjs';
import { AmendCommand } from './commands/AmendCommand.mjs';
import { VerdictCommand } from './commands/VerdictCommand.mjs';
import { DupeCommand } from './commands/DupeCommand.mjs';
import { ResolveCommand } from './commands/ResolveCommand.mjs';
import { ThreadCommand } from './commands/ThreadCommand.mjs';
import { BlameCommand } from './commands/BlameCommand.mjs';
import { SummaryCommand, StopCommand } from './commands/NoteCommands.mjs';
import { ListCommand } from './commands/ListCommand.mjs';

export function createCommands() {
  const commands = [];
  const commandByName = (name) => commands.find((command) => command.name === name) ?? null;

  commands.push(
    new AssessCommand(),
    new BatchCommand(commandByName),
    new FindingCommand(),
    new AmendCommand(),
    new VerdictCommand(),
    new DupeCommand(),
    new ResolveCommand(),
    new ThreadCommand(),
    new BlameCommand(),
    new SummaryCommand(),
    new StopCommand(),
    new ListCommand(),
  );

  return commands;
}
