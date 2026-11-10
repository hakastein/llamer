import { fileURLToPath } from 'node:url';
import { HARNESS } from '../agent/stageBin.mjs';

// Слово без кавычек — только из букв, которые оболочка не раскрывает.
const WORD = String.raw`[\w./:,@+=%-]+`;
// Одинарные кавычки, склеенные апострофом `\'` или `"'"`: внутри них оболочка
// не трогает ничего, переводы строк тоже.
const QUOTED = String.raw`'[^']*'(?:(?:\\'|"'")'[^']*')*`;
const CALL = new RegExp(`^${HARNESS} [a-z]+(?:[ \\t]+(?:${QUOTED}|${WORD}))*[ \\t]*$`);
// Цикл по литеральным путям: в слове списка нет ничего, что оболочка раскрыла бы.
const PATH = String.raw`[A-Za-z0-9_./@+-]+`;
const SEP = String.raw`[ \t]*[;\n]\s*`;
const ARG = String.raw`(?:"\$\k<name>"|\$\k<name>|${PATH})`;
const LOOP = new RegExp(String.raw`^for[ \t]+(?<name>[A-Za-z_]\w*)[ \t]+in(?:[ \t]+${PATH})+${SEP}do\s+${HARNESS} diff(?:[ \t]+${ARG})+${SEP}done(?:[ \t]*\|[ \t]*(?:head|tail) -\d+)?\s*$`);

/**
 * Вызов харнесса — одна простая команда без раскрытий. Такой Claude Code
 * пропускает правилом `Bash(review … *)` сам, но команду длиннее 10 000 символов
 * он не разбирает вовсе и в dontAsk отклоняет: батч из полутора десятков
 * находок пропадал целиком. Хук разрешает её по той же форме, что и правило.
 * Ещё — цикл `review diff` по путям: подстановку `"$f"` Claude Code не разбирает
 * и правилом `Bash(review diff *)` не пропускает.
 */
export function isHarnessCall(command) {
  return CALL.test(command) || LOOP.test(command);
}

async function main() {
  let raw = '';
  for await (const chunk of process.stdin) raw += chunk;
  const command = JSON.parse(raw).tool_input?.command ?? '';
  // Молчание — решение за правилами разрешений.
  if (!isHarnessCall(command)) return;
  process.stdout.write(JSON.stringify({
    hookSpecificOutput: {
      hookEventName: 'PreToolUse', permissionDecision: 'allow', permissionDecisionReason: `вызов ${HARNESS}`,
    },
  }));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) await main();
