import { spawnSync } from 'node:child_process';

// Окружение этапа — белый список: токены GitLab и трекера, переменные CI и всё
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

export function binaryGap(name, settings, env) {
  const probe = spawnSync(settings.bin, ['--version'], { encoding: 'utf-8', env });
  if (probe.status === 0) return [];
  return [{
    id: `driver-${name}`,
    message: `драйвер ${name}: ${settings.bin} не запускается (${probe.error?.code ?? `код ${probe.status}`})`,
  }];
}

/**
 * Общее у драйверов: уровень глубины → модель × усилие и блок скиллов в задании.
 * Как этап берёт скилл, говорит наследник: `skillsHow` и `skillRef(name)`.
 */
export class StageDriver {
  #settings;

  constructor(settings) {
    this.#settings = settings;
  }

  get settings() { return this.#settings; }

  level(number) { return this.#settings.levels[number - 1]; }

  skillsBlock(library, names) {
    if (names.length === 0) return '';
    library.assertPresent(names);
    return '## Твой метод\n\n'
      + `${this.skillsHow} Это твой метод,\n`
      + 'он обязателен, и ниже его содержание не повторяется.\n\n'
      + names.map((name) => `- ${this.skillRef(name)}`).join('\n');
  }
}
