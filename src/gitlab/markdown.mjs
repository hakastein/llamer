const REVISION_MARK = 'llamer-rev';

/**
 * GitLab рендерит `$…$` как формулу KaTeX, и текст с переменными (`$order`,
 * `$sum`) превращается в кашу из курсива и подстрочных. Внутри backtick-спанов
 * не трогаем: там `$` и так не рендерится, а `\$` показался бы буквально.
 */
export function escapeDollarsOutsideCode(text) {
  return String(text)
    .split(/(`+[^`]*`+)/)
    .map((part, i) => (i % 2 === 0 ? part.replace(/\$/g, '\\$') : part))
    .join('');
}

export function formatRevisionMark(headSha) {
  return `<sub>ревизия <code>${String(headSha).slice(0, 8)}</code></sub>`
    + `<!-- ${REVISION_MARK}:${headSha} -->`;
}

export function withRevisionMark(body, headSha) {
  return headSha ? `${body}\n\n${formatRevisionMark(headSha)}` : body;
}

export function collectRevisionMarks(bodies) {
  const pattern = new RegExp(`<!--\\s*${REVISION_MARK}:([0-9a-f]{7,40})\\s*-->`, 'g');
  const found = new Set();
  for (const body of bodies) {
    for (const match of String(body ?? '').matchAll(pattern)) found.add(match[1]);
  }
  return [...found];
}

/**
 * Правка, применяемая кнопкой. Смещения в `suggestion:-N+M` GitLab считает от
 * строки, на которой стоит комментарий, а у многострочного треда это последняя
 * строка диапазона: замена ровно выделенного фрагмента — это N = длина диапазона
 * минус один и M = 0.
 */
export function withSuggestion(text, { line, lineEnd, suggestion }) {
  // Ограждение длиннее любой последовательности бэктиков внутри кода, иначе
  // блок закроется раньше времени.
  const longest = Math.max(0, ...(suggestion.match(/`+/g) ?? []).map((run) => run.length));
  const fence = '`'.repeat(Math.max(3, longest + 1));
  const above = lineEnd - line;
  // Пустая правка — блок без содержимого: кнопка удаляет выделенные строки.
  const content = suggestion === '' ? '' : `${suggestion}\n`;

  return `${text}\n\n${fence}suggestion:-${above}+0\n${content}${fence}`;
}

/**
 * Тело для замечания, которое не встало на строки: место переезжает в текст,
 * иначе автор читает замечание, не зная, о каком месте речь. Правка кнопкой
 * из такого комментария не применяется, поэтому она идёт обычным блоком кода.
 */
export function unanchoredBody(finding, markSha = null) {
  const range = finding.lineEnd && finding.lineEnd !== finding.line
    ? `${finding.line}-${finding.lineEnd}`
    : String(finding.line);
  const place = `**\`${finding.file}:${range}\`**`
    + (finding.deleted ? ' (удалённые строки)' : '');
  const parts = [`${place} — замечание не встало на строки реквеста`,
    escapeDollarsOutsideCode(finding.text)];
  if (finding.suggestion != null) {
    parts.push(`Предлагаемая замена:\n\n\`\`\`\n${finding.suggestion}\n\`\`\``);
  }
  return withRevisionMark(parts.join('\n\n'), markSha);
}

/**
 * Тело инлайн-треда: текст замечания, правка кнопкой, метка ревизии.
 *
 * Экранируется только текст замечания: блок `suggestion` GitLab обязан прочитать
 * дословно, иначе кнопка «применить» подставит в код экранирующие обратные слэши.
 */
export function findingBody(finding, markSha = null) {
  const text = escapeDollarsOutsideCode(finding.text);
  return withRevisionMark(
    finding.suggestion != null ? withSuggestion(text, finding) : text,
    markSha,
  );
}
