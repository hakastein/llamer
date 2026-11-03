import { CLAUDE_DEFAULTS, ClaudeDriver, claudeGaps } from './claude.mjs';
import { CODEX_DEFAULTS, CodexDriver, codexGaps } from './codex.mjs';

/**
 * Встроенные драйверы. Интерфейс один: `prepare` готовит песочницу,
 * `launch` собирает запуск этапа (команда, аргументы, окружение, stdin и разбор
 * потока в события claude), `level` переводит уровень глубины в модель × усилие,
 * `skillsBlock` говорит этапу, как взять скиллы.
 */
const DRIVERS = {
  claude: { defaults: CLAUDE_DEFAULTS, Driver: ClaudeDriver, gaps: claudeGaps },
  codex: { defaults: CODEX_DEFAULTS, Driver: CodexDriver, gaps: codexGaps },
};

export const DRIVER_DEFAULTS = Object.fromEntries(
  Object.entries(DRIVERS).map(([name, { defaults }]) => [name, defaults]),
);

export function prepareDriver(name, options) {
  return DRIVERS[name].Driver.prepare(options);
}

export function driverGaps(name, settings, env) {
  return DRIVERS[name].gaps(settings, env);
}
