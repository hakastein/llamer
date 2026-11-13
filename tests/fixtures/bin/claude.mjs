// Заглушка claude: те же аргументы и stdin, что у настоящего, отчёт — теми же
// командами харнесса. С чем этап был запущен, пишется в $TMPDIR/launches.jsonl:
// окружение этапа — белый список, и TMPDIR в нём есть.
import { spawn, spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import {
  appendFileSync, existsSync, mkdirSync, readdirSync, readFileSync, renameSync, writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { stubCommands } from '../stub-plan.mjs';
import { SECRETS } from '../repo.mjs';

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
const pluginAt = argv.indexOf('--plugin-dir');
const pluginSkills = pluginAt === -1 ? null : join(argv[pluginAt + 1], 'skills');

appendFileSync(join(tmpdir(), 'launches.jsonl'), `${JSON.stringify({
  driver: 'claude',
  stage: brief.stage,
  argv,
  env: Object.keys(process.env).sort(),
  prompt,
  commands: brief.commands,
  help: spawnSync('review', ['--help'], { encoding: 'utf-8', env: process.env }).stdout,
  diff: spawnSync('review', ['diff', 'src/Order.php'], { encoding: 'utf-8', env: process.env }).stdout,
  shared: sharedAt === -1 ? null : readFileSync(argv[sharedAt + 1], 'utf-8'),
  configDir: process.env.CLAUDE_CONFIG_DIR,
  skills: pluginSkills && existsSync(pluginSkills) ? readdirSync(pluginSkills).sort() : null,
})}\n`);

// Сессия, как у Claude Code: projects/<каталог>/<id>.jsonl конфига, в ней — вывод команды с токеном.
const sessionId = `stub-${brief.stage}`;
const project = join(process.env.CLAUDE_CONFIG_DIR, 'projects', '-stub-tree');
mkdirSync(project, { recursive: true });
writeFileSync(join(project, `${sessionId}.jsonl`), `${JSON.stringify({ type: 'user', text: `токен ${SECRETS.GITLAB_TOKEN}` })}\n`);
emit({
  type: 'system', subtype: 'init', model: 'stub', session_id: sessionId,
});
// Лимиты подписки — дословное событие живого claude 2.1.316.
emit(JSON.parse('{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794480600,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.45,"resetsAt":1794480600,"status":"allowed"},"seven_day":{"utilization":0.58,"resetsAt":1794787200,"status":"allowed"}}},"uuid":"a7255113-cdfd-4eac-9f94-7a154dcadb53","session_id":"f66bac7e-9b13-4b63-82a4-ae9fce05e825"}'));

// STUB_HANG_STAGE: этап с запущенной командой повисает — его прерывает тест; через минуту оба выходят сами.
if (process.env.STUB_HANG_STAGE === brief.stage) {
  const command = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 60000)'], { stdio: 'ignore' });
  const hang = join(tmpdir(), 'hang.json');
  writeFileSync(`${hang}.part`, JSON.stringify({ llamer: process.ppid, stage: process.pid, command: command.pid }));
  renameSync(`${hang}.part`, hang);
  setTimeout(() => process.exit(1), 60000);
  await new Promise(() => {});
}

// STUB_FAIL_STAGE задаётся конфигом теста через drivers.<имя>.env: этап срывается, ничего не записав.
if (process.env.STUB_FAIL_STAGE === brief.stage) {
  emit({ type: 'result', is_error: true, result: 'заглушка: этап сорвался' });
  process.exit(1);
}
emit({ type: 'assistant', message: { id: 'm1', content: [{ type: 'text', text: `заглушка этапа ${brief.stage}` }] } });

// STUB_DENY_STAGE: вызов харнесса отклонён правами, как батч длиннее предела разбора claude.
if (process.env.STUB_DENY_STAGE === brief.stage) {
  emit({ type: 'assistant', message: { id: 'cdeny', content: [{ type: 'tool_use', id: 'deny', name: 'Bash', input: { command: "review batch --json '[]'" } }] } });
  emit({ type: 'system', subtype: 'permission_denied', tool_name: 'Bash', tool_use_id: 'deny' });
  emit({
    type: 'user',
    message: { content: [{ type: 'tool_result', tool_use_id: 'deny', is_error: true, content: 'Permission to use Bash has been denied' }] },
  });
}

let failed = 0;
for (const args of stubCommands(brief)) {
  // По имени из PATH, как настоящий claude: так тест видит, что обёртка харнесса на месте.
  const done = spawnSync('review', args, { encoding: 'utf-8', env: process.env });
  emit({ type: 'assistant', message: { id: `c${args[0]}`, content: [{ type: 'tool_use', name: 'Bash', input: { command: `review ${args[0]}` } }] } });
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
