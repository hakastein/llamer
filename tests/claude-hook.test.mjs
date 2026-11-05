import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { ClaudeDriver } from '../src/drivers/claude.mjs';
import { isHarnessCall } from '../src/drivers/claudeHook.mjs';

// Хук — ровно тот, что драйвер кладёт в --settings этапа.
function hookCommand() {
  const { argv } = new ClaudeDriver({ settings: { bin: 'claude', env: {} }, env: {}, runSkills: null }).launch({
    level: { model: 'm', effort: 'e' }, projectMemory: false, commands: ['batch'], allow: [], shared: null, prompt: '', env: {},
  });
  const { hooks } = JSON.parse(argv[argv.indexOf('--settings') + 1]);
  return hooks.PreToolUse[0].hooks[0].command;
}

const ask = (command) => spawnSync('sh', ['-c', hookCommand()], {
  input: JSON.stringify({ hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command } }),
  encoding: 'utf-8',
});

// Форма батча из срыва: многострочный JSON с кириллицей и кодом, длиннее
// 10 000 символов — предела, за которым Claude Code команду не разбирает.
const finding = (n) => `{"type":"finding","file":"src/A.php","line":${n},"text":"Ключ '"'"'identity'"'"' => $analysis['\\''map'\\''] и \\"кавычки\\" в \`коде\`"}`;
const BATCH = `review batch --json '[\n${Array.from({ length: 120 }, (_, n) => finding(n)).join(',\n')}\n]'`;

test('хук этапа claude разрешает длинный многострочный батч харнесса', () => {
  assert.ok(BATCH.length > 10_000);
  const answer = ask(BATCH);
  assert.equal(answer.status, 0, answer.stderr);
  assert.equal(JSON.parse(answer.stdout).hookSpecificOutput.permissionDecision, 'allow');
});

test('хук молчит обо всём, что не одна простая команда харнесса', () => {
  for (const command of [
    "review batch --json 'x'; rm -rf ~",
    "review batch --json 'x'\nrm -rf ~",
    "review batch --json 'x' && id",
    "review batch --json 'x' ; id",
    "review batch --json 'x'$'\\x41'",
    "review batch --json 'x' | sh",
    "review batch --json 'x' > /tmp/f",
    'review batch --json "$(id)"',
    "review batch --json 'x'$(id)",
    "review batch --json 'x'`id`",
    "review batch --json $HOME",
    "review batch --json 'незакрытая",
    "cat x | review batch --json 'x'",
    "reviewer batch --json 'x'",
  ]) {
    assert.equal(isHarnessCall(command), false, command);
  }
  assert.equal(ask("review batch --json 'x'; rm -rf ~").stdout, '');
});
