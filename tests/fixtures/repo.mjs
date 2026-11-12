import { execFileSync, spawn } from 'node:child_process';
import {
  existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const LLAMER = new URL('../../bin/llamer.mjs', import.meta.url).pathname;
const STUB_BIN = new URL('./bin', import.meta.url).pathname;

export const SECRETS = {
  GITLAB_TOKEN: 'glpat-stub-token-must-not-leak',
};

/** Ветка с незакоммиченным против main — как разбирает свою ветку разработчик. */
export const WORKTREE = ['--exclude', 'main', '--head', 'worktree'];

const made = [];
process.on('exit', () => {
  for (const dir of made) rmSync(dir, { recursive: true, force: true });
});

export function tempDir(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  made.push(dir);
  return dir;
}

export function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
}

export function write(root, path, text) {
  mkdirSync(join(root, path, '..'), { recursive: true });
  writeFileSync(join(root, path), text);
}

export const BUILTIN = ['assess', 'threads', 'logic', 'quality', 'comments', 'tests', 'dedupe', 'skeptic'];

/**
 * Этапы конфига: все встроенные и `own` поверх них. Драйверы этапа — из
 * `drivers` по ключу, иначе свои из `own`, иначе `drivers.all` (claude).
 */
export function stages(own = {}, { all = ['claude'], ...byKey } = {}) {
  const merged = { ...Object.fromEntries(BUILTIN.map((key) => [key, {}])), ...own };
  return Object.fromEntries(Object.entries(merged)
    .map(([key, stage]) => [key, { ...stage, drivers: byKey[key] ?? stage.drivers ?? all }]));
}

export const CONFIG = {
  version: 2,
  driver: 'claude',
  skillPaths: ['skills'],
  allow: ['rg'],
  stages: stages({
    logic: { skills: { always: ['reviewing'], onDemand: ['backend'] }, allow: ['git log'] },
    skeptic: { skills: { always: ['refuting'] } },
    docs: { task: 'Документация.', paths: ['docs/**'] },
  }),
};

/** Лестница из пяти уровней на моделях `<prefix>-<номер>`. */
export function levels(prefix = 'model') {
  return [1, 2, 3, 4, 5].map((n) => ({ model: `${prefix}-${n}`, effort: `effort-${n}` }));
}

/**
 * Ветка feature/dev-7-orders ответвилась от main, после чего main ушла вперёд:
 * чужая правка из main в разбор попадать не должна. В рабочем дереве —
 * незакоммиченная правка и неотслеживаемый файл.
 */
export function makeRepo({ config = CONFIG } = {}) {
  const root = tempDir('llamer-repo-');
  git(root, 'init', '-q', '-b', 'main');
  git(root, 'config', 'user.email', 'dev@example.com');
  git(root, 'config', 'user.name', 'dev');
  write(root, '.llamer.json', JSON.stringify(config, null, 2));
  write(root, 'skills/reviewing/SKILL.md', '---\nname: reviewing\n---\nМетод разбора.\n');
  write(root, 'skills/refuting/SKILL.md', '---\nname: refuting\n---\nМетод скептика.\n');
  write(root, 'skills/backend/SKILL.md', '---\nname: backend\ndescription: "Use when the request touches PHP"\n---\nСоглашения бэкенда.\n');
  write(root, 'src/Order.php', '<?php\n\nfunction total() {\n    return 1;\n}\n');
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'init');

  git(root, 'checkout', '-q', '-b', 'feature/dev-7-orders');
  write(root, 'src/Order.php', '<?php\n\nfunction total() {\n    return 2;\n}\n');
  git(root, 'commit', '-q', '-am', 'feature');

  git(root, 'checkout', '-q', 'main');
  write(root, 'src/Foreign.php', '<?php\n\nfunction foreign() {}\n');
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'main moved on');
  git(root, 'checkout', '-q', 'feature/dev-7-orders');

  write(root, 'src/Order.php', '<?php\n\nfunction total() {\n    return 3;\n}\n');
  write(root, 'src/Fresh.php', '<?php\n\nfunction fresh() {\n    return 4;\n}\n');
  return root;
}

/** Окружение прогона на заглушках: свой HOME, без личных кредов и конфигов. */
export function stubEnv(extra = {}) {
  const home = tempDir('llamer-home-');
  const codexHome = join(home, '.codex');
  mkdirSync(codexHome, { recursive: true });
  writeFileSync(join(codexHome, 'auth.json'), '{"auth_mode":"stub"}');
  const env = {
    ...process.env,
    HOME: home,
    PATH: `${STUB_BIN}:${process.env.PATH}`,
    CLAUDE_CODE_OAUTH_TOKEN: 'stub-oauth-token-value',
    CODEX_HOME: codexHome,
    TMPDIR: home,
    ...SECRETS,
    ...extra,
  };
  for (const name of ['CLAUDE_CONFIG_DIR', 'GITLAB_URL', 'CI_SERVER_URL', 'ANTHROPIC_API_KEY', 'OPENAI_API_KEY', 'CODEX_API_KEY']) {
    if (!(name in extra)) delete env[name];
  }
  return env;
}

/**
 * Прогон llamer отдельным процессом. Асинхронно: заглушка GitLab живёт в
 * процессе теста, и синхронный spawn заблокировал бы её цикл событий.
 */
export function run(root, args, extraEnv = {}) {
  const env = stubEnv(extraEnv);
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [LLAMER, ...args], { cwd: root, env });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('close', (status) => {
      const log = join(env.TMPDIR, 'launches.jsonl');
      const launches = existsSync(log)
        ? readFileSync(log, 'utf-8').trim().split('\n').map((line) => JSON.parse(line))
        : [];
      resolve({
        status, stdout, stderr, launches, env,
      });
    });
  });
}
