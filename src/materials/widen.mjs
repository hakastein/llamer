import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function splitLines(text) {
  const lines = text.split('\n');
  return lines[lines.length - 1] === '' ? lines.slice(0, -1) : lines;
}

/**
 * Старая версия файла из новой и узкого диффа: добавленные строки убираются,
 * удалённые возвращаются на свои места.
 */
export function restoreOldVersion(newLines, added, removed) {
  const kept = [];
  for (let line = 1; line <= newLines.length; line += 1) {
    if (!added.has(line)) kept.push({ newLine: line, text: newLines[line - 1] });
  }
  const old = [];
  let taken = 0;
  const byOldLine = [...removed.entries()].sort((a, b) => a[0] - b[0]);
  for (const [, entry] of byOldLine) {
    while (taken < kept.length && kept[taken].newLine <= entry.newLine) {
      old.push(kept[taken].text);
      taken += 1;
    }
    old.push(entry.text ?? '');
  }
  for (; taken < kept.length; taken += 1) old.push(kept[taken].text);
  return old;
}

function gitDiffFiles(oldPath, newPath, context) {
  try {
    execFileSync('git', ['diff', '--no-index', `-U${context}`, oldPath, newPath], {
      encoding: 'utf-8', maxBuffer: 64 * 1024 * 1024,
    });
    return '';
  } catch (error) {
    // Различия git отдаёт кодом 1 — текст диффа лежит в stdout ошибки.
    if (error.status !== 1) throw error;
    return String(error.stdout ?? '');
  }
}

function retitle(diffText, file, { born }) {
  return diffText
    .split('\n')
    .filter((line) => !line.startsWith('index ') && !line.startsWith('similarity '))
    .map((line) => {
      if (line.startsWith('diff --git ')) return `diff --git a/${file} b/${file}`;
      if (line.startsWith('--- ')) return born ? '--- /dev/null' : `--- a/${file}`;
      if (line.startsWith('+++ ')) return `+++ b/${file}`;
      return line;
    })
    .join('\n')
    .trimEnd();
}

export function widenFromWorkTree({
  files, added, removed, read, context,
}) {
  const dir = mkdtempSync(join(tmpdir(), 'review-widen-'));
  try {
    const chunks = [];
    for (const file of files) {
      const text = read(file);
      if (text === null) continue;

      const newLines = splitLines(text);
      const addedHere = added.get(file) ?? new Map();
      const removedHere = removed.get(file) ?? new Map();
      if (addedHere.size === 0 && removedHere.size === 0) continue;

      const oldPath = join(dir, 'old');
      const newPath = join(dir, 'new');
      const oldLines = restoreOldVersion(newLines, addedHere, removedHere);
      writeFileSync(oldPath, oldLines.length === 0 ? '' : `${oldLines.join('\n')}\n`);
      writeFileSync(newPath, `${newLines.join('\n')}\n`);
      const chunk = retitle(gitDiffFiles(oldPath, newPath, context), file, {
        born: oldLines.length === 0,
      });
      if (chunk) chunks.push(chunk);
    }
    return chunks.join('\n');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
