import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const FINDING_PAD = 3;
const WINDOW_FALLBACK = 6;
const CONTEXT_CAP = 40;

export function fileReader(cwd) {
  const cache = new Map();
  return (file) => {
    if (!cache.has(file)) {
      try {
        cache.set(file, readFileSync(join(cwd, file), 'utf-8'));
      } catch {
        cache.set(file, null);
      }
    }
    return cache.get(file);
  };
}

export function extractFindingContext(fileText, lineStart, lineEnd) {
  const lines = fileText.split('\n');
  const from = Math.max(1, lineStart - FINDING_PAD);
  const to = Math.min(lines.length, (lineEnd || lineStart) + FINDING_PAD);
  const body = [];
  for (let i = from; i <= to; i += 1) body.push(`L${i}\t${lines[i - 1] ?? ''}`);
  return body.join('\n');
}

function braceBodyEnd(lines, from, cap) {
  let depth = 0;
  let seen = false;
  let end = from - 1;
  for (let i = from; i <= cap; i += 1) {
    for (const char of lines[i - 1] ?? '') {
      if (char === '{') {
        depth += 1;
        seen = true;
      } else if (char === '}') depth -= 1;
    }
    end = i;
    if (seen && depth <= 0) break;
  }
  return end;
}

/**
 * Фрагменты «комментарий + код под ним»: смежные строки-комментарии (разрыв ≤1)
 * образуют шапку, за ней идёт тело до закрытия скобки либо окно в
 * WINDOW_FALLBACK строк.
 *
 * Пометка `L<n>+` обязательна: комментарий, чья шапка попала внутрь окна
 * предыдущего фрагмента, иначе неотличим от старого окружения — и этап его
 * не разбирает.
 */
export function extractCommentContext(fileText, commentLines) {
  const lines = fileText.split('\n');
  const isAdded = new Set(commentLines);
  const blocks = [];
  for (const number of [...commentLines].sort((a, b) => a - b)) {
    const last = blocks[blocks.length - 1];
    if (last && number <= last.commentEnd + 1) last.commentEnd = number;
    else blocks.push({ commentStart: number, commentEnd: number });
  }

  const fragments = [];
  let prevEnd = 0;
  for (const block of blocks) {
    let bodyStart = block.commentEnd + 1;
    while (bodyStart <= lines.length && lines[bodyStart - 1]?.trim() === '') bodyStart += 1;
    const opensBrace = bodyStart <= lines.length
      && bodyStart <= block.commentEnd + 3
      && (lines.slice(block.commentEnd, bodyStart).join('') + (lines[bodyStart - 1] ?? '')).includes('{');
    const end = opensBrace
      ? braceBodyEnd(lines, bodyStart, Math.min(lines.length, block.commentStart + CONTEXT_CAP))
      : Math.min(lines.length, block.commentEnd + WINDOW_FALLBACK);
    const start = Math.max(prevEnd + 1, block.commentStart);
    if (start > end) continue;

    const body = [];
    for (let i = start; i <= end; i += 1) {
      body.push(`L${i}${isAdded.has(i) ? '+' : ''}\t${lines[i - 1] ?? ''}`);
    }
    fragments.push(body.join('\n'));
    prevEnd = end;
  }
  return fragments.join('\n  …\n');
}

/**
 * Обогащает находки кодом с диска: `context` — окно вокруг самой находки,
 * `relatedContext` — окна по местам, названным её автором в `--related`.
 * Нечитаемый файл пропускается: скептик дочитает сам.
 *
 * Находка на удалённый код остаётся без окна: её строки — из старой версии,
 * и вырезка из файла на диске подсунула бы скептику чужой код.
 */
export function findingContexts(findings, cwd) {
  const read = fileReader(cwd);
  return findings.map((finding) => {
    const enriched = { ...finding };
    const text = finding.file && finding.line && !finding.deleted ? read(finding.file) : null;
    if (text !== null) {
      enriched.context = extractFindingContext(text, finding.line, finding.lineEnd ?? finding.line);
    }
    if (Array.isArray(finding.related) && finding.related.length > 0) {
      enriched.relatedContext = finding.related.map((related) => {
        const relatedText = related.line ? read(related.file) : null;
        return {
          file: related.file,
          snippet: relatedText
            ? extractFindingContext(relatedText, related.line, related.lineEnd ?? related.line)
            : null,
        };
      });
    }
    return enriched;
  });
}

/** Добавленные комментарии реквеста с кодом под ними — материал этапа комментариев. */
export function commentsContext(diff, cwd) {
  const read = fileReader(cwd);
  const parts = [];
  for (const [file, commentLines] of diff.addedCommentLines()) {
    const text = read(file);
    if (text === null) continue;
    const fragment = extractCommentContext(text, commentLines);
    if (fragment) parts.push(`${file}\n${fragment}`);
  }
  return parts.join('\n\n');
}
