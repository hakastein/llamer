#!/usr/bin/env node
import { Refusal } from '../domain/Refusal.mjs';
import { Journal } from '../domain/journal.mjs';
import { StageBrief } from './StageBrief.mjs';
import { HarnessContext } from './HarnessContext.mjs';
import { createCommands } from './createCommands.mjs';
import { runHarness } from './program.mjs';

function main(argv) {
  const brief = StageBrief.read(process.env);
  const context = new HarnessContext({
    brief, journal: new Journal(brief.journalDir, brief.stage), cwd: process.cwd(),
  });
  const outcome = runHarness({ argv, commands: createCommands(), context });
  if (outcome.print !== undefined) {
    console.log(outcome.print);
    return;
  }
  const { command, result } = outcome;
  if (result.print !== undefined) {
    console.log(result.print);
    return;
  }
  for (const event of result.events) context.journal.append({ ...event, stage: context.stage });
  console.log(`записано: ${command.name}${result.confirmation}`);
}

// Отказ агенту и сбой харнесса — разные исходы: первый адресован агенту и
// чинится исправленным вызовом, во втором чинить вызов нечего.
try {
  main(process.argv.slice(2));
} catch (error) {
  if (error instanceof Refusal) {
    console.error(`отказ: ${error.message}`);
    process.exit(1);
  }
  console.error(`сбой харнесса: ${error.stack ?? error.message}`);
  process.exit(2);
}
