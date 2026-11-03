import { createHash } from 'node:crypto';

/**
 * Код строки диффа в терминах GitLab: `sha1(путь)_<старая>_<новая>`. Пару даёт
 * разбор диффа — у добавленной строки старый номер это счётчик старого файла
 * на её позиции, у удалённой наоборот.
 */
export function lineCode(file, { oldLine, newLine }) {
  return `${createHash('sha1').update(file).digest('hex')}_${oldLine}_${newLine}`;
}

/**
 * Позиция инлайн-треда. Комментарий всегда несёт диапазон: GitLab ставит ноту
 * на последнюю строку и подсвечивает от line_range.start до неё; диапазон
 * в одну строку — частный случай той же разметки.
 */
export function buildThreadPosition({
  file, line, lineEnd, deleted = false, diff, version,
}) {
  const start = Number(line);
  const end = Number(lineEnd);

  for (const number of [start, end]) {
    if (!diff.hasLine(file, number, { deleted })) {
      throw new Error(
        `строка ${file}:${number} не найдена среди ${deleted ? 'удалённых' : 'изменённых'}`
        + ' — код строки для GitLab не собрать',
      );
    }
  }

  const anchor = (number) => {
    const code = lineCode(file, diff.pairAt(file, number, { deleted }));
    return deleted
      ? { line_code: code, type: 'old', old_line: number, new_line: null }
      : { line_code: code, type: 'new', old_line: null, new_line: number };
  };

  return {
    position_type: 'text',
    base_sha: version.baseSha,
    head_sha: version.headSha,
    start_sha: version.startSha,
    new_path: file,
    old_path: diff.oldPathOf(file),
    ...(deleted ? { old_line: end, new_line: null } : { new_line: end }),
    line_range: { start: anchor(start), end: anchor(end) },
  };
}

/**
 * GitLab не отказывает на непонятой позиции, а создаёт обычный тред к MR
 * целиком: публикация выглядит успешной, а комментарий висит ни на какой
 * строке, и правка в нём не применяется кнопкой.
 */
export function assertAnchored(thread, file, line) {
  const note = thread?.notes?.[0];
  if (note?.type !== 'DiffNote' || !note?.position?.line_range) {
    throw new Error(
      `тред ${file}:${line} создан без привязки к строкам (тип ${note?.type ?? 'неизвестен'}):`
      + ' GitLab не принял позицию. Предложенная правка в таком треде не применяется',
    );
  }
  return thread;
}

const removedAt = (diff, file, newLine) => [...(diff.removed.get(file) ?? [])]
  .filter(([, entry]) => entry.newLine === newLine)
  .sort(([a], [b]) => a - b);

/**
 * Удалённая строка в изменениях задачи пронумерована от их базы, а тред встаёт
 * на старую сторону диффа реквеста — от другой базы. В обоих диффах она стоит
 * перед той же новой строкой и несёт тот же текст; блок одинаковых удалённых
 * строк сопоставляется по порядку.
 */
export function deletedLineInRequest({
  file, line, taskAnchors, requestDiff,
}) {
  const number = Number(line);
  const entry = taskAnchors.removed.get(file)?.get(number);
  if (!entry) throw new Error(`удалённой строки ${file}:${number} нет в изменениях задачи`);

  const ours = removedAt(taskAnchors, file, entry.newLine);
  const theirs = removedAt(requestDiff, file, entry.newLine);
  const sameBlock = ours.length === theirs.length
    && ours.every(([, a], index) => a.text === theirs[index][1].text);
  if (sameBlock) return theirs[ours.findIndex(([at]) => at === number)][0];

  const matches = theirs.filter(([, candidate]) => candidate.text === entry.text);
  if (matches.length === 1) return matches[0][0];
  throw new Error(`удалённая строка ${file}:${number} не найдена в диффе реквеста`);
}
