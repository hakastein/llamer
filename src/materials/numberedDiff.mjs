const HUNK = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

// Шире пяти знаков номера строк в репозитории не бывает; ровная колонка
// держит маркер `+`/`-` на одном месте, и строку читать как обычный дифф.
const WIDTH = 5;

/**
 * Дифф пакета с номером строки текущего дерева у каждой строки ханка.
 *
 * Сырой unified diff даёт номер только в заголовке `@@`, и позицию строки агент
 * получает отсчётом от него — на деле он его не делает, а ищет имя текстом.
 * Номер у строки — готовый `path:line` для инструментов навигации и для
 * `llamer-harness`. У удалённой строки в дереве нет места, номер ей не ставится.
 */
export function numberDiffLines(text) {
  let line = null;
  return text.split('\n').map((row) => {
    if (row.startsWith('diff --git ')) {
      line = null;
      return row;
    }
    const hunk = HUNK.exec(row);
    if (hunk) {
      line = Number(hunk[1]);
      return row;
    }
    if (line === null || row.startsWith('\\')) return row;
    if (row.startsWith('-')) return `${' '.repeat(WIDTH)} ${row}`;
    if (row.startsWith('+') || row.startsWith(' ')) {
      const numbered = `${String(line).padStart(WIDTH)} ${row}`;
      line += 1;
      return numbered;
    }
    // Заголовки файла (`---`/`+++`) идут до первого ханка, номер ещё не задан;
    // сюда доходят только служебные строки, которых в ханке не бывает.
    return row;
  }).join('\n');
}
