import { formatSignalsSummary } from '../materials/signals.mjs';
import { LEVEL_COUNT } from '../config.mjs';

export const ASSESSMENT_MARKER = '<!-- llamer-assessment -->';

const NATURE = { mechanical: 'механическое', behavioral: 'содержательное' };
const RISK = { low: 'низкий', medium: 'средний', high: 'высокий' };

export function buildRiskNote({ assessment, signals, reviewed = '' }) {
  const parts = [
    '**Оценка перед разбором**',
    '',
    `Изменение ${NATURE[assessment.nature] ?? assessment.nature}, риск ${RISK[assessment.risk] ?? assessment.risk}.`,
    '',
    `Обоснование: ${assessment.reason}`,
    '',
    `Разбор идёт на уровне ${assessment.level} из ${LEVEL_COUNT}.`,
    '',
    formatSignalsSummary(signals),
  ];
  if (reviewed) parts.push('', reviewed);
  return parts.join('\n');
}
