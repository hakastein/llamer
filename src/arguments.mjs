import { parseArgs } from 'node:util';

export const HELP = `llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.

Что разбирается:
  --mr <url|project!iid>      merge request в GitLab (нужен GITLAB_TOKEN; для project!iid — GITLAB_URL);
                              исключается целевая ветка MR — origin/<целевая> должна быть в репозитории
  --head <ревизия|worktree|index>
                              голова разбора: ревизия, рабочее дерево с неотслеживаемыми или индекс
  --base <ревизия>            разбор ровно от этой ревизии
  --exclude <ревизия>         всё, что достижимо из ревизии, в разбор не идёт; повторяемый, с --base не сочетается
  --task <файл>               текст задачи: этапы сверяют с ним реализацию

Без --mr нужны --head и ровно одно из --base или --exclude; с --mr --head и --base не задаются.

Выход:
  по умолчанию                YAML-результат в stdout и в .llamer/<прогон>/result.yaml, отчёт report.md рядом
  --publish gitlab            треды, заметка о рисках, сводка и статус в MR (только с --mr)
  --dry-run                   ничего не публиковать: отчёт вместо --publish (только с --mr)
  --since-last                только изменения после прошлого прогона LLaMer в MR (только с --mr)

Прочее:
  --config <файл>             конфиг вместо .llamer.json в корне репозитория; .llamer.local.json рядом с ним накладывается
  --preflight                 проверить окружение и напечатать YAML пробелов, прогон не начинать
  -h, --help                  эта справка
`;

const OPTIONS = {
  mr: { type: 'string' },
  base: { type: 'string' },
  head: { type: 'string' },
  exclude: { type: 'string', multiple: true },
  task: { type: 'string' },
  publish: { type: 'string' },
  'dry-run': { type: 'boolean' },
  'since-last': { type: 'boolean' },
  config: { type: 'string' },
  preflight: { type: 'boolean' },
  help: { type: 'boolean', short: 'h' },
};

export class UsageError extends Error {}

export function parseArguments(argv) {
  let values;
  try {
    ({ values } = parseArgs({
      args: argv, options: OPTIONS, strict: true, allowPositionals: false,
    }));
  } catch (error) {
    throw new UsageError(`${error.message} — справка: llamer --help`);
  }
  if (values.help) return { help: true };

  const mr = values.mr !== undefined;
  const exclude = values.exclude ?? [];
  if (mr && (values.head !== undefined || values.base !== undefined)) {
    throw new UsageError('с --mr база и голова берутся из merge request: --head и --base не задаются');
  }
  if (!mr && values.head === undefined) throw new UsageError('нужен --mr или --head — справка: llamer --help');
  if (values.base !== undefined && exclude.length > 0) throw new UsageError('--base и --exclude не сочетаются: нужно одно из них');
  if (!mr && values.base === undefined && exclude.length === 0) {
    throw new UsageError('с --head нужно одно из --base или --exclude — справка: llamer --help');
  }
  if (values.publish !== undefined && values.publish !== 'gitlab') {
    throw new UsageError(`--publish: поддерживается только gitlab; пришло «${values.publish}»`);
  }
  for (const flag of ['publish', 'dry-run', 'since-last']) {
    if (values[flag] !== undefined && !mr) throw new UsageError(`--${flag} — только вместе с --mr`);
  }

  return {
    help: false,
    input: mr ? 'gitlab' : 'revisions',
    mr: values.mr ?? null,
    base: values.base ?? null,
    head: values.head ?? null,
    exclude,
    task: values.task ?? null,
    publish: values.publish !== undefined && !values['dry-run'],
    sinceLast: Boolean(values['since-last']),
    config: values.config ?? null,
    preflight: Boolean(values.preflight),
  };
}
