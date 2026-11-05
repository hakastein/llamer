import { readFileSync } from 'node:fs';
import { readRunState } from '../../src/domain/journal.mjs';
import { Diff } from '../../src/materials/Diff.mjs';
import { SECRETS } from './repo.mjs';

/**
 * Что заглушка драйвера делает на каждом этапе. Суждения о коде в ней нет:
 * находки заданы заранее, но пишутся теми же командами харнесса, что у модели,
 * и проходят те же проверки.
 */
const PLAN = {
  assess: () => [[
    'assess', '--nature', 'behavioral', '--risk', 'medium', '--level', '2',
    '--reason', 'заглушка: вердикт задан заранее',
  ]],

  threads: ({ brief }) => brief.threads.map((thread) => [
    'resolve', '--id', thread.id, '--reason', `заглушка: замечание снято правкой, токен ${SECRETS.GITLAB_TOKEN}`,
  ]),

  dedupe: ({ state }) => {
    const kept = new Map();
    const commands = [];
    for (const finding of state.findings) {
      const place = `${finding.file}:${finding.line}`;
      if (!kept.has(place)) {
        kept.set(place, finding.id);
        continue;
      }
      commands.push(['dupe', '--id', String(finding.id), '--of', String(kept.get(place)),
        '--reason', 'заглушка: то же место']);
    }
    return commands;
  },

  // STUB_SKEPTIC_REFUTES задаётся конфигом теста через drivers.<имя>.env.
  skeptic: ({ state }) => state.keptFindings.map((finding) => [
    'verdict', '--id', String(finding.id), '--refuted', process.env.STUB_SKEPTIC_REFUTES ? 'true' : 'false',
    '--reason', `заглушка решила, токен ${SECRETS.GITLAB_TOKEN}`,
  ]),
};

// Разбирающий этап: находка на первой изменённой строке первого файла и сводка, если она у этапа есть.
// В тексте находки — токен GitLab, как если бы модель вычитала его из дерева:
// наружу он должен уйти вымаранным.
// STUB_STOP_STAGE задаётся конфигом теста через drivers.<имя>.env: этап прекращает работу командой stop;
// STUB_NO_SUMMARY — этап пишет находку и выходит без сводки.
function analysis({ brief }) {
  if (process.env.STUB_STOP_STAGE === brief.stage) return [['stop', '--reason', 'заглушка: часть кода не разобрана']];
  const diff = Diff.fromText(readFileSync(brief.diffPath, 'utf-8'));
  const file = diff.files.find((candidate) => diff.linesOf(candidate).length > 0);
  const commands = [];
  if (file) {
    const line = String(diff.linesOf(file)[0]);
    commands.push([
      'finding', '--file', file, '--line', line, '--line-end', line,
      '--text', `заглушка этапа ${brief.stage} отметила ${file}:${line}, токен ${SECRETS.GITLAB_TOKEN}`,
      '--severity', 'major', '--severity-reason', 'заглушка ставит вес',
      '--related', `${file}:${line}`,
    ]);
  }
  if (brief.commands.includes('summary') && process.env.STUB_NO_SUMMARY !== brief.stage) {
    commands.push(['summary', '--text', `заглушка этапа ${brief.stage}: разбора кода не было`]);
  }
  return commands;
}

export function stubCommands(brief) {
  const state = readRunState(brief.journalDir);
  return (PLAN[brief.stage] ?? analysis)({ brief, state });
}
