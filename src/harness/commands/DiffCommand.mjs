import { Command } from '../Command.mjs';
import { git } from '../../git.mjs';
import { Diff } from '../../materials/Diff.mjs';
import { numberDiffLines } from '../../materials/numberedDiff.mjs';
import { DIFF_WIDTHS } from '../../materials/gather.mjs';

const STATUS = {
  A: 'добавлен', M: 'изменён', D: 'удалён', R: 'переименован',
};

/** Файлы изменений задачи по ревизиям задания: путь → строка перечня и путь до переименования. */
function changedFiles({ revs, cwd }) {
  const lines = (args) => git(['diff', ...args, ...revs], cwd).split('\n').filter(Boolean);
  // Обе выдачи — из одной очереди git diff, порядок файлов в них один.
  const counts = lines(['--numstat']);
  return new Map(lines(['--name-status']).map((line, index) => {
    const [status, path, renamedTo] = line.split('\t');
    const [plus, minus] = counts[index].split('\t');
    const kind = STATUS[status[0]] ?? status;
    const lineCount = plus === '-' ? 'бинарный' : `+${plus} −${minus}`;
    const file = renamedTo ?? path;
    return [file, { line: `${file} — ${kind}${renamedTo ? ` из ${path}` : ''}, ${lineCount}`, from: path }];
  }));
}

export class DiffCommand extends Command {
  get name() {
    return 'diff';
  }

  get summary() {
    return "the task's diff of the given files, numbered and widened as in the pack's diff";
  }

  get manual() {
    return 'Several paths per call. Without paths: every changed file with its status and line counts.';
  }

  get positionals() {
    return '[path...]';
  }

  get prints() {
    return true;
  }

  build(values, context) {
    const files = changedFiles(context);
    const paths = values.positionals ?? [];
    if (paths.length === 0) return { print: [...files.values()].map(({ line }) => line).join('\n') || 'изменений задачи нет' };
    return {
      print: paths.map((path) => (files.has(path)
        // Тот же вызов, что строит дифф пакета, с первой — полной — шириной контекста.
        ? numberDiffLines(new Diff({
          baseRef: context.revs[0], files: [path], revs: context.revs, cwd: context.cwd, renamedFrom: new Map([[path, files.get(path).from]]),
        })
          .text({ context: DIFF_WIDTHS[0] })).trimEnd()
        : `${path}: среди изменений задачи такого файла нет; перечень — review diff без аргументов`)).join('\n'),
    };
  }
}
