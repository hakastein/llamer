// Конфиги инструментов массовой автоправки: их правка в реквесте — признак механики.
const MECHANICAL_CONFIGS = ['rector.php', '.php-cs-fixer.dist.php', '.php-cs-fixer.php'];

// 300 файлов с одним паттерном — одна правка, повторённая 300 раз, а не 300
// разных. Вход обязан быть `git diff -U0`: на широком контексте соседние
// правки объединяются в один ханк, и функция теряет точность.
export function mechanicalShare(diff) {
  // Хвост строки заголовка ханка ("-1,1 +1,1 @@") нужно съесть целиком —
  // иначе он остаётся первой строкой тела и похож на удалённую строку кода.
  const hunks = diff.split(/^@@[^\n]*\n/m).slice(1);
  if (hunks.length === 0) return 0;

  const shapes = new Map();
  let singleLine = 0;
  for (const hunk of hunks) {
    const lines = hunk.split('\n');
    const added = lines.filter((line) => /^\+[^+]/.test(line));
    const removed = lines.filter((line) => /^-[^-]/.test(line));
    if (added.length !== 1 || removed.length > 1) continue;
    singleLine += 1;
    const shape = added[0].replace(/[\wА-я]+/g, 'x').trim();
    shapes.set(shape, (shapes.get(shape) ?? 0) + 1);
  }
  if (singleLine === 0) return 0;
  const repeated = [...shapes.values()].filter((count) => count > 1).reduce((a, b) => a + b, 0);
  return repeated / hunks.length;
}

export function formatSignalsSummary(signals) {
  return `Файлов ${signals.size.files}, изменённых строк ${signals.size.changedLines}.`
    + ` Тесты в реквесте: ${signals.hasTests ? 'есть' : 'нет'}.\n`
    + `Признаки механической правки: доля однотипных ханков ${signals.mechanical.share.toFixed(2)}`
    + `, конфиги ${signals.mechanical.configs.join(', ') || 'нет'}`
    + `, маркеров @generated ${signals.mechanical.generatedMarkers}.`;
}

/**
 * Строки с маркером `@generated` среди ДОБАВЛЕННЫХ. Считать по тексту диффа
 * нельзя: в контекстных строках маркер принадлежит чужому, нетронутому коду.
 */
function generatedMarkers(diff) {
  let count = 0;
  for (const lines of diff.added.values()) {
    for (const { text } of lines.values()) if (text.includes('@generated')) count += 1;
  }
  return count;
}

export function collectRiskSignals(diff) {
  const { files } = diff;
  return {
    size: { files: files.length, changedLines: diff.changedLineCount },
    mechanical: {
      // Узкий дифф: mechanicalShare считает ханки, и ширина контекста меняет их
      // границы, а с ними и результат.
      share: mechanicalShare(diff.text({ context: 0 })),
      configs: files.filter((file) => MECHANICAL_CONFIGS.includes(file.split('/').pop())),
      generatedMarkers: generatedMarkers(diff),
    },
    // Сегмент пути, а не префикс от корня: тесты лежат и не под корневым tests/.
    hasTests: files.some((file) => file.split('/').includes('tests')),
  };
}
