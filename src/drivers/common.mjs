import { spawnSync } from 'node:child_process';
import {
  existsSync, mkdirSync, readdirSync, readFileSync, statSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { dirname, join } from 'node:path';
import { redactSecrets } from '../redact.mjs';

// Окружение этапа — белый список: токен GitLab, переменные CI и всё
// прочее, чего здесь нет, в процесс модели не попадает. Нужное сверх этого
// драйверу задаёт `drivers.<имя>.env` конфига.
const BASE_ENV = ['PATH', 'HOME', 'LANG', 'TZ', 'TMPDIR', 'USER', 'SHELL', 'TERM',
  // сеть и доверие: без них драйвер за прокси не достучится до своего API
  ...['HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'ALL_PROXY'].flatMap((name) => [name, name.toLowerCase()]),
  'NODE_EXTRA_CA_CERTS', 'SSL_CERT_FILE', 'SSL_CERT_DIR'];

/** Базовые переменные, вход драйвера (`auth`) и каталог команд этапа первым в PATH. */
export function stageEnvOf(env, stageBin, auth) {
  const stageEnv = Object.fromEntries(Object.entries(env).filter(([name]) => BASE_ENV.includes(name)
    || name.startsWith('LC_') || auth.includes(name)));
  stageEnv.PATH = `${stageBin}:${env.PATH ?? ''}`;
  return stageEnv;
}

/** Каталог `dir` со ссылками на скиллы этапа из каталога скиллов прогона — и ни на какие больше. */
export function linkSkills(dir, runSkills, names) {
  mkdirSync(dir, { recursive: true });
  for (const name of names) symlinkSync(join(runSkills, 'skills', name), join(dir, name));
}

/** Существующие каталоги `rel` в cwd и выше до корня репозитория: там драйверы сами ищут скиллы дерева. */
export function upToRepoRoot(cwd, rel) {
  const found = [];
  for (let dir = cwd; ; dir = dirname(dir)) {
    found.push(join(dir, rel));
    if (existsSync(join(dir, '.git')) || dirname(dir) === dir) return found.filter((path) => existsSync(path));
  }
}

/**
 * Копия файла или каталога с вымаранными секретами: журнал сессии этапа уходит в
 * артефакты, а в нём вывод команд агента. Файлы читаются текстом.
 */
export function keepRedacted(from, to) {
  if (!existsSync(from)) return;
  if (statSync(from).isDirectory()) {
    for (const name of readdirSync(from)) keepRedacted(join(from, name), join(to, name));
    return;
  }
  mkdirSync(dirname(to), { recursive: true });
  writeFileSync(to, redactSecrets(readFileSync(from, 'utf-8')));
}

export function binaryGap(name, settings, env) {
  const probe = spawnSync(settings.bin, ['--version'], { encoding: 'utf-8', env });
  if (probe.status === 0) return [];
  return [{
    id: `driver-${name}`,
    message: `драйвер ${name}: ${settings.bin} не запускается (${probe.error?.code ?? `код ${probe.status}`})`,
  }];
}

/**
 * Общее у драйверов: блок скиллов в задании. Как этап берёт скилл, говорит
 * наследник: `skillsHow` и `skillRef(name)`; как пишется одинарная кавычка
 * внутри текста в вызове — `quote`.
 */
export class StageDriver {
  #settings;

  constructor(settings) {
    this.#settings = settings;
  }

  get settings() { return this.#settings; }

  get quote() { return "'\\''"; }

  // Скиллы onDemand в задании не перечисляются: claude и codex сами видят скиллы
  // этапа с описаниями из frontmatter и берут подходящие к реквесту.
  skillsBlock(library, { always, onDemand }) {
    library.assertPresent([...always, ...onDemand]);
    if (always.length === 0) return '';
    return '## Your Method\n\n'
      + `${this.skillsHow} They are your method,\n`
      + 'they are not optional, and nothing below repeats what is in them.\n\n'
      + always.map((name) => `- ${this.skillRef(name)}`).join('\n');
  }
}
