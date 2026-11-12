import { summariseThreads } from './threads.mjs';
import { collectRevisionMarks } from '../gitlab/markdown.mjs';
import { STATUS_MARKER } from '../report/ReviewReport.mjs';
import { collectRiskSignals } from './signals.mjs';
import { ContextPack, PACK_LIMIT } from './ContextPack.mjs';
import { numberDiffLines } from './numberedDiff.mjs';
import { taskChanges } from './taskChanges.mjs';
import { Diff } from './Diff.mjs';
import { Materials } from './Materials.mjs';
import { log } from '../log.mjs';

export const DIFF_WIDTHS = [10, 6, 3, 0];

/** Остаток пакета под задачу и треды. */
const PACK_RESERVE = 60_000;
const DIFF_BUDGET = PACK_LIMIT - PACK_RESERVE;

/**
 * Фиксированная ширина тратит бюджет на окружение, а режется дифф с конца:
 * последние файлы до этапа не доезжают вовсе. В бюджет меряется дифф уже с
 * номерами строк — таким он и уходит в пакет.
 *
 * Полный дифф, когда влезает, идёт как есть. Иначе первыми место уступают
 * удалённые файлы: их содержимое — сплошь минусы, и этап берёт его через
 * `review diff`, а ширина подбирается уже по свёрнутому диффу.
 */
export function widestFittingDiff(diff, budget) {
  const full = numberDiffLines(diff.text({ context: DIFF_WIDTHS[0] }));
  if (full.length <= budget) return { text: full, context: DIFF_WIDTHS[0], folded: 0, saved: 0 };
  for (const context of DIFF_WIDTHS) {
    const numbered = numberDiffLines(diff.text({ context }));
    const { text, folded } = foldDeleted(numbered);
    if (text.length <= budget || context === DIFF_WIDTHS.at(-1)) {
      return {
        text, context, folded, saved: numbered.length - text.length,
      };
    }
  }
}

/** Удалённый файл — его заголовки и одна строка-заглушка вместо содержимого. */
export function foldDeleted(text) {
  let folded = 0;
  const chunks = text.split(/(?=^diff --git )/m).map((chunk) => {
    // Строки ханка в пакете начинаются с отступа номера: с начала строки идут только заголовки.
    const hunk = chunk.search(/^@@ /m);
    const head = chunk.slice(0, hunk);
    if (hunk < 0 || !/^\+\+\+ \/dev\/null$/m.test(head)) return chunk;
    const path = /^--- "?a\/(.*?)"?$/m.exec(head)?.[1];
    const lines = /^@@ -1(?:,(\d+))? /m.exec(chunk.slice(hunk))?.[1] ?? 1;
    const stub = `${head}@@ -1,${lines} +0,0 @@ deleted file, ${lines} lines removed; the content: review diff ${path}\n`;
    // Короткий файл заглушка только удлинила бы.
    if (stub.length >= chunk.length) return chunk;
    folded += 1;
    return stub;
  });
  return { text: chunks.join(''), folded };
}

async function collectThreads(request) {
  if (request.kind !== 'gitlab') return [];
  try {
    const threads = summariseThreads(await request.discussions());
    if (threads.length > 0) log(`уже сказанное в MR: ${threads.length} замечаний по строкам`);
    return threads;
  } catch (error) {
    log(`треды MR прочитать не удалось (${error.message}) — разбор может повторить сказанное`);
    return [];
  }
}

/**
 * Метки ревизии со сводок прошлых прогонов. Только из заметок, начинающихся
 * маркером статуса: цитата сводки в ответе человека несёт ту же метку.
 */
async function previousRunMarks(request) {
  try {
    const notes = (await request.discussions()).flatMap((discussion) => discussion.notes ?? []);
    return collectRevisionMarks(notes
      .filter((note) => !note.system && String(note.body ?? '').startsWith(STATUS_MARKER))
      .map((note) => note.body));
  } catch (error) {
    log(`заметки MR для --since-last прочитать не удалось (${error.message}) — разбирается весь реквест`);
    return [];
  }
}

/**
 * Дешёвая часть: дифф реквеста и изменения задачи. Решение «есть ли что
 * разбирать» принимается по ней — до сбора пакета.
 *
 * Разбирается не дифф реквеста, а изменения задачи; дифф реквеста остаётся
 * для публикации: треды GitLab встают только на его строки.
 *
 * @throws {import('./taskChanges.mjs').TaskChangesError}
 */
export async function planReviewScope({
  request, tree, sinceLast = false,
}) {
  const requestDiff = await request.requestDiff(tree.path);
  const task = taskChanges({
    cwd: tree.path,
    head: request.headSha,
    excludes: request.excludes,
    requestDiff,
    requestBase: request.baseSha,
    explicitBase: request.explicitBase,
    sinceMarks: sinceLast ? await previousRunMarks(request) : null,
  });
  return {
    requestDiff, task, diff: task.diff, nothingOwn: task.diff.isEmpty,
  };
}

export async function gatherMaterials({
  request, tree, scope, issue, artifacts,
}) {
  const { requestDiff, task, diff } = scope;
  if (issue) log(`текст задачи: ${issue.length} символов`);

  const signals = collectRiskSignals(diff);
  log(`размер изменений: ${signals.size.files} файлов, ${signals.size.changedLines} изменённых строк`);

  const {
    text: diffText, context: diffWidth, folded, saved,
  } = widestFittingDiff(diff, DIFF_BUDGET);
  if (folded > 0) log(`удалённые файлы свёрнуты в заглушки: ${folded}, сэкономлено ${saved} символов`);
  log(`дифф в пакет: ${diffText.length} символов, контекст ${diffWidth} строк`);

  const threads = await collectThreads(request);

  if (requestDiff.sourceText) artifacts.saveDiff('request', requestDiff.sourceText);
  // Контекст как у GitLab (три строки): номер новой строки у удалённой строки
  // в таком диффе совпадает с диффом реквеста, по нему удалённая строка
  // переносится на строку реквеста при публикации.
  const taskText = task.diff.text({ context: 3 });

  const materials = new Materials({
    diff,
    signals,
    issue,
    threads,
    requestDiff,
    task,
    taskAnchors: Diff.fromText(taskText, { baseRef: task.baseSha }),
    diffPath: artifacts.saveDiff('task', taskText),
    cwd: tree.path,
    pack: new ContextPack({
      diff: diffText,
      folded,
      issue,
      threads,
    }),
  });
  log(`пакет контекста: общая часть ${materials.sharedPack.length} символов,`
    + ` часть этапа ${materials.renderPack().length}`);
  return materials;
}
