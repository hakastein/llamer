import { parseArgs } from 'node:util';

export const HELP = `llamer — агентное ревью изменений: оценка, разбирающие этапы, дедупликация, скептик.

Вход (ровно один):
  --mr <url|project!iid>      merge request в GitLab (нужен GITLAB_TOKEN; для project!iid — GITLAB_URL)
  --local [--target <ветка>]  рабочее дерево против целевой ветки, вместе с незакоммиченным
  --base <sha> --head <sha>   две ревизии репозитория

Выход:
  по умолчанию                отчёт: report.md в .llamer/<прогон>/ и JSON находок в stdout
  --publish gitlab            треды, заметка о рисках, сводка и статус в MR (только с --mr)
  --dry-run                   ничего не публиковать: отчёт вместо --publish

Прочее:
  --config <файл>             конфиг вместо .llamer.json в корне репозитория
  --preflight                 проверить окружение и напечатать JSON пробелов, прогон не начинать
  -h, --help                  эта справка
`;

const OPTIONS = {
  mr: { type: 'string' },
  local: { type: 'boolean' },
  target: { type: 'string' },
  base: { type: 'string' },
  head: { type: 'string' },
  publish: { type: 'string' },
  'dry-run': { type: 'boolean' },
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

  const range = values.base !== undefined || values.head !== undefined;
  const inputs = [values.mr !== undefined, Boolean(values.local), range].filter(Boolean).length;
  if (inputs !== 1) throw new UsageError('нужен ровно один вход: --mr, --local или --base с --head — справка: llamer --help');
  if (range && (values.base === undefined || values.head === undefined)) {
    throw new UsageError('--base и --head идут парой');
  }
  if (values.target !== undefined && !values.local) throw new UsageError('--target — только вместе с --local');
  if (values.publish !== undefined && values.publish !== 'gitlab') {
    throw new UsageError(`--publish: поддерживается только gitlab; пришло «${values.publish}»`);
  }
  if (values.publish !== undefined && values.mr === undefined) {
    throw new UsageError('--publish gitlab публикует в merge request — нужен --mr');
  }

  let input = 'range';
  if (values.mr !== undefined) input = 'gitlab';
  else if (values.local) input = 'local';

  return {
    help: false,
    input,
    mr: values.mr ?? null,
    target: values.target ?? null,
    base: values.base ?? null,
    head: values.head ?? null,
    publish: values.publish !== undefined && !values['dry-run'],
    config: values.config ?? null,
    preflight: Boolean(values.preflight),
  };
}
