import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const FINDING_PAD = 3;

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
