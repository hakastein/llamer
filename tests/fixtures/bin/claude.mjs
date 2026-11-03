// Заглушка claude: те же аргументы и stdin, что у настоящего, отчёт — теми же
// командами харнесса. С чем этап был запущен, пишется в $TMPDIR/launches.jsonl:
// окружение этапа — белый список, и TMPDIR в нём есть.
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { appendFileSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { stubCommands } from '../stub-plan.mjs';

if (process.argv[2] === '--version') {
  console.log('0.0.0 (заглушка claude)');
  process.exit(0);
}

const emit = (event) => process.stdout.write(`${JSON.stringify(event)}\n`);
const argv = process.argv.slice(2);
const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const prompt = Buffer.concat(chunks).toString('utf-8');
const brief = JSON.parse(readFileSync(process.env.LLAMER_BRIEF, 'utf-8'));
const sharedAt = argv.indexOf('--append-system-prompt-file');

appendFileSync(join(tmpdir(), 'launches.jsonl'), `${JSON.stringify({
  driver: 'claude',
  stage: brief.stage,
  argv,
  env: Object.keys(process.env).sort(),
  prompt,
  shared: sharedAt === -1 ? null : readFileSync(argv[sharedAt + 1], 'utf-8'),
  configDir: process.env.CLAUDE_CONFIG_DIR,
})}\n`);

emit({ type: 'system', subtype: 'init', model: 'stub' });

// STUB_FAIL_STAGE задаётся конфигом теста через drivers.<имя>.env: этап срывается, ничего не записав.
if (process.env.STUB_FAIL_STAGE === brief.stage) {
  emit({ type: 'result', is_error: true, result: 'заглушка: этап сорвался' });
  process.exit(1);
}
emit({ type: 'assistant', message: { id: 'm1', content: [{ type: 'text', text: `заглушка этапа ${brief.stage}` }] } });

let failed = 0;
for (const args of stubCommands(brief)) {
  // По имени из PATH, как настоящий claude: так тест видит, что обёртка харнесса на месте.
  const done = spawnSync('llamer-harness', args, { encoding: 'utf-8', env: process.env });
  emit({ type: 'assistant', message: { id: `c${args[0]}`, content: [{ type: 'tool_use', name: 'Bash', input: { command: `llamer-harness ${args[0]}` } }] } });
  if (done.status !== 0) failed += 1;
  emit({
    type: 'user',
    message: { content: [{ type: 'tool_result', is_error: done.status !== 0, content: `${done.stdout}${done.stderr}` }] },
  });
}

emit({
  type: 'result',
  is_error: failed > 0,
  result: failed > 0 ? `отказов харнесса: ${failed}` : 'готово',
  total_cost_usd: 0.01,
  modelUsage: {
    stub: {
      inputTokens: 1200, outputTokens: 300, cacheReadInputTokens: 4000, cacheCreationInputTokens: 200,
    },
  },
});
process.exit(failed > 0 ? 1 : 0);
