// Заглушка `codex exec --json`: события в формате codex, журнал — харнессом из PATH.
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import {
  appendFileSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { stubCommands } from '../stub-plan.mjs';
import { SECRETS } from '../repo.mjs';

if (process.argv[2] === '--version') {
  console.log('codex-cli 0.0.0-stub');
  process.exit(0);
}

const emit = (event) => process.stdout.write(`${JSON.stringify(event)}\n`);
const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const prompt = Buffer.concat(chunks).toString('utf-8');
const brief = JSON.parse(readFileSync(process.env.LLAMER_BRIEF, 'utf-8'));
const rules = join(process.env.CODEX_HOME ?? '', 'rules', 'llamer.rules');
const skills = join(process.env.HOME ?? '', '.agents', 'skills');
const config = join(process.env.CODEX_HOME ?? '', 'config.toml');

appendFileSync(join(tmpdir(), 'launches.jsonl'), `${JSON.stringify({
  driver: 'codex',
  stage: brief.stage,
  argv: process.argv.slice(2),
  env: Object.keys(process.env).sort(),
  prompt,
  home: process.env.HOME,
  codexHome: process.env.CODEX_HOME,
  auth: existsSync(join(process.env.CODEX_HOME ?? '', 'auth.json')),
  rules: existsSync(rules) ? readFileSync(rules, 'utf-8') : null,
  skills: existsSync(skills) ? readdirSync(skills).sort() : null,
  config: existsSync(config) ? readFileSync(config, 'utf-8') : null,
})}\n`);

// Журнал сессии, как у codex: по записи на запрос, сумма — в turn.completed ниже;
// между ними вывод команды с токеном, как если бы агент вычитал его из дерева.
if (!process.argv.includes('--ephemeral')) {
  const sessions = join(process.env.CODEX_HOME, 'sessions', '2026', '11', '05');
  mkdirSync(sessions, { recursive: true });
  writeFileSync(join(sessions, 'rollout-stub.jsonl'), [
    { type: 'token_usage_record', payload: { usage: { input_tokens: 2000, cached_input_tokens: 1500, output_tokens: 100 } } },
    { type: 'response_item', payload: { type: 'function_call_output', output: `токен ${SECRETS.GITLAB_TOKEN}` } },
    { type: 'token_usage_record', payload: { usage: { input_tokens: 3000, cached_input_tokens: 2500, output_tokens: 200, reasoning_output_tokens: 50 } } },
  ].map((record) => JSON.stringify(record)).join('\n'));
}

emit({ type: 'thread.started', thread_id: `stub-${brief.stage}` });
emit({ type: 'turn.started' });
emit({ type: 'item.completed', item: { id: 'm0', type: 'agent_message', text: `заглушка этапа ${brief.stage}` } });

let failed = 0;
for (const [index, args] of stubCommands(brief).entries()) {
  const command = `review ${args[0]} …`;
  emit({ type: 'item.started', item: { id: `c${index}`, type: 'command_execution', command } });
  const done = spawnSync('review', args, { encoding: 'utf-8', env: process.env });
  if (done.status !== 0) failed += 1;
  emit({
    type: 'item.completed',
    item: {
      id: `c${index}`, type: 'command_execution', command, exit_code: done.status, aggregated_output: `${done.stdout}${done.stderr}`,
    },
  });
}
emit(failed > 0
  ? { type: 'turn.failed', error: { message: `отказов харнесса: ${failed}` } }
  : {
    type: 'turn.completed',
    usage: {
      input_tokens: 5000, cached_input_tokens: 4000, output_tokens: 300,
    },
  });
process.exit(failed > 0 ? 1 : 0);
