const SPECIAL = /[.+^${}()|[\]\\]/g;

// `**` покрывает любое число сегментов вместе с разделителем, `*` — часть
// одного сегмента: `src/*.php` не должен доставать `src/deep/A.php`.
function maskToRegExp(mask) {
  const source = mask
    .replace(SPECIAL, '\\$&')
    .split('**')
    .map((part) => part.replace(/\*/g, '[^/]*'))
    .join('.*')
    .replace(new RegExp('\\.\\*/', 'g'), '(?:.*/)?');
  return new RegExp(`^${source}$`);
}

const cache = new Map();

function regExpFor(mask) {
  let compiled = cache.get(mask);
  if (!compiled) {
    compiled = maskToRegExp(mask);
    cache.set(mask, compiled);
  }
  return compiled;
}

/**
 * Файл подходит, если подходит под маску без `!` и ни под одну с `!`, как в
 * `.gitignore`. Без масок без `!` подходит всё: этап без `paths` разбирает любые файлы.
 */
export function matchesAnyMask(masks, files) {
  const include = masks.filter((mask) => !mask.startsWith('!'));
  const exclude = masks.filter((mask) => mask.startsWith('!')).map((mask) => mask.slice(1));
  const hits = (list, file) => list.some((mask) => regExpFor(mask).test(file));
  return files.some((file) => (include.length === 0 || hits(include, file)) && !hits(exclude, file));
}
