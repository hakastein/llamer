import { pluralRu } from '../plural.mjs';

export const SCOPE_HEADING = '<details><summary>Что ревьюилось';

const files = (count) => `${count} ${pluralRu(count, 'файл', 'файла', 'файлов')}`;

function origin({ kind, bases }) {
  if (kind === 'explicit') return `Дифф от базы \`${bases[0].sha.slice(0, 8)}\`, заданной \`--base\`.`;
  const branches = bases.map(({ branch }) => `\`${branch}\``).join(', ');
  return `Изменения задачи: всё, чего ещё нет в ${branches}.`;
}

function fileLine(diff, file, conflicted) {
  const added = diff.added.get(file)?.size ?? 0;
  const removed = diff.removed.get(file)?.size ?? 0;
  return `- \`${file}\` +${added} −${removed}${conflicted.has(file) ? ' — по диффу реквеста' : ''}`;
}

/**
 * Сворачиваемый блок «что разбиралось и от чего»: читатель видит, почему часть
 * файлов реквеста ревьюер не тронул, и может повторить дифф руками.
 */
export function buildScopeBlock({ task }) {
  const { diff } = task;
  const conflicted = new Set(task.conflicted);
  const parts = [
    `${SCOPE_HEADING}: ${files(diff.files.length)}</summary>`,
    '',
    origin(task),
  ];
  if (task.kind !== 'explicit') {
    parts.push('', `Базы: ${task.bases.map(({ branch, sha }) => `\`${branch}@${sha.slice(0, 8)}\``).join(', ')}.`);
  }
  if (conflicted.size > 0) {
    parts.push('', `Между общими ветками ${pluralRu(conflicted.size, 'конфликтует', 'конфликтуют', 'конфликтуют')}`
      + ` ${files(conflicted.size)}: по ним разобран дифф реквеста.`);
  }
  if (diff.files.length > 0) {
    parts.push('', ...diff.files.map((file) => fileLine(diff, file, conflicted)));
  }
  if (task.excluded.length > 0) {
    parts.push(
      '',
      `Отсеяно как ${task.since ? 'чужие или уже разобранные прошлым прогоном' : 'чужие'} изменения: ${files(task.excluded.length)} диффа реквеста.`,
      '',
      '<details><summary>Отсеянные файлы</summary>',
      '',
      ...task.excluded.map((file) => `- \`${file}\``),
      '',
      '</details>',
    );
  }
  parts.push('', '</details>');
  return parts.join('\n');
}
