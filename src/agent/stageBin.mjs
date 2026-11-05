import { chmodSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const HARNESS = 'review';

const HARNESS_MAIN = fileURLToPath(new URL('../harness/main.mjs', import.meta.url));

export const quoted = (value) => `'${String(value).replaceAll("'", "'\\''")}'`;

/**
 * Каталог команд этапа: обёртка `review` над тем же node и тем же
 * харнессом, что ведут прогон. Пишется на каждый прогон, а не лежит в пакете:
 * установка из тарбола не обязана сохранить бит исполнения, а `env node`
 * в окружении этапа мог бы найти другой node.
 */
export function prepareStageBin(dir) {
  mkdirSync(dir, { recursive: true });
  const path = join(dir, HARNESS);
  writeFileSync(path, `#!/bin/sh\nexec ${quoted(process.execPath)} ${quoted(HARNESS_MAIN)} "$@"\n`);
  chmodSync(path, 0o755);
  return dir;
}
