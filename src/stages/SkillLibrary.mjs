import {
  cpSync, existsSync, readdirSync,
} from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const SKILL_FILE = 'SKILL.md';

/** Встроенные скиллы — `skills/` пакета. */
export const BUILTIN_SKILLS = fileURLToPath(new URL('../../skills', import.meta.url));

/**
 * Скиллы этапов: встроенные и из каталогов `skillPaths`, скилл — подкаталог с
 * SKILL.md. Одно имя в двух каталогах `skillPaths` — ошибка: какой метод получит
 * этап, решал бы порядок каталогов, а не тот, кто писал конфиг. Встроенный скилл
 * одноимённым из `skillPaths` не подменяется и ошибкой не считается.
 */
export class SkillLibrary {
  #index;

  constructor(index) {
    this.#index = index;
  }

  static load(paths) {
    const index = new Map();
    const builtin = new Set();
    for (const root of [BUILTIN_SKILLS, ...paths]) {
      if (!existsSync(root)) throw new Error(`каталога скиллов ${root} нет`);
      for (const entry of readdirSync(root, { withFileTypes: true })) {
        const dir = join(root, entry.name);
        if (!(entry.isDirectory() || entry.isSymbolicLink()) || !existsSync(join(dir, SKILL_FILE))) continue;
        if (builtin.has(entry.name)) continue;
        if (root === BUILTIN_SKILLS) builtin.add(entry.name);
        if (index.has(entry.name)) {
          throw new Error(`скилл ${entry.name} есть в двух каталогах: ${index.get(entry.name)} и ${dir}`);
        }
        index.set(entry.name, dir);
      }
    }
    return new SkillLibrary(index);
  }

  dirOf(name) {
    return this.#index.get(name) ?? null;
  }

  missing(names) {
    return names.filter((name) => !this.dirOf(name));
  }

  // Пропажу скилла ловим на сборке промпта: у агента она стоила бы хода и
  // осталась бы в логе видимостью работы.
  assertPresent(names) {
    const missing = this.missing(names);
    if (missing.length > 0) throw new Error(`скиллы не найдены: ${missing.join(', ')}`);
  }

  /**
   * Скиллы прогона в `<dir>/skills/<имя>`: копия, а не ссылка — каталог ложится в
   * артефакты и показывает текст метода, с которым шли этапы, даже после
   * обновления скиллов. Как отдать каталог этапу, решает драйвер.
   */
  materialize(dir, names) {
    this.assertPresent(names);
    for (const name of names) {
      cpSync(this.dirOf(name), join(dir, 'skills', name), { recursive: true, dereference: true });
    }
    return dir;
  }
}
