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
 */
export function widestFittingDiff(diff, budget) {
  for (const context of DIFF_WIDTHS) {
    const text = numberDiffLines(diff.text({ context }));
    if (text.length <= budget) return { text, context };
  }
  return { text: numberDiffLines(diff.text({ context: 0 })), context: 0 };
}

async function collectIssueText(tracker, issueId) {
  if (!issueId || tracker.missing().length > 0) return '';
  try {
    const text = await tracker.read(issueId);
    if (text) log(`задача прочитана: ${text.length} символов`);
    return text ?? '';
  } catch (error) {
    log(`задачу ${issueId} прочитать не удалось (${error.message})`
      + ' — сверки реализации с требованиями не будет');
    return '';
  }
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
  request, tree, config, sinceLast = false,
}) {
  const requestDiff = await request.requestDiff(tree.path);
  const task = taskChanges({
    cwd: tree.path,
    head: request.headSha,
    targetBranch: request.targetBranch,
    sharedBranches: config.sharedBranches,
    requestDiff,
    requestBase: request.baseSha,
    explicitBase: request.kind === 'range' ? request.baseSha : null,
    fetch: request.kind === 'gitlab',
    sinceMarks: sinceLast ? await previousRunMarks(request) : null,
  });
  return {
    requestDiff, task, diff: task.diff, nothingOwn: task.diff.isEmpty,
  };
}

export async function gatherMaterials({
  request, tree, scope, tracker, artifacts,
}) {
  const { requestDiff, task, diff } = scope;

  const issueId = tracker.issueIdOf(request.sourceBranch);
  if (tracker.type !== 'none') log(issueId ? `задача: ${issueId}` : 'задача в имени ветки не найдена');

  const signals = collectRiskSignals(diff);
  log(`размер изменений: ${signals.size.files} файлов, ${signals.size.changedLines} изменённых строк`);

  const { text: diffText, context: diffWidth } = widestFittingDiff(diff, DIFF_BUDGET);
  log(`дифф в пакет: ${diffText.length} символов, контекст ${diffWidth} строк`);

  const threads = await collectThreads(request);

  if (requestDiff.sourceText) artifacts.saveDiff('request', requestDiff.sourceText);
  // Контекст как у GitLab (три строки): номер новой строки у удалённой строки
  // в таком диффе совпадает с диффом реквеста, по нему удалённая строка
  // переносится на строку реквеста при публикации.
  const taskText = task.diff.text({ context: 3 });
  const issue = await collectIssueText(tracker, issueId);

  const materials = new Materials({
    diff,
    signals,
    // Без текста задачи номер этапу не нужен: обещал бы задачу, которой в пакете нет.
    issueId: issue ? issueId : null,
    threads,
    requestDiff,
    task,
    taskAnchors: Diff.fromText(taskText, { baseRef: task.baseSha }),
    diffPath: artifacts.saveDiff('task', taskText),
    cwd: tree.path,
    pack: new ContextPack({
      diff: diffText,
      issue,
      threads,
    }),
  });
  log(`пакет контекста: общая часть ${materials.sharedPack.length} символов,`
    + ` часть этапа ${materials.renderPack().length}`);
  return materials;
}
