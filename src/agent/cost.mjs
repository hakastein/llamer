import { writeFileSync } from 'node:fs';

export function tokensOf(usage) {
  if (!usage) return 0;
  return Object.values(usage).reduce((sum, model) => sum
    + (model.inputTokens ?? 0) + (model.outputTokens ?? 0)
    + (model.cacheReadInputTokens ?? 0) + (model.cacheCreationInputTokens ?? 0), 0);
}

function peakContextTokens(stages) {
  return Math.max(0, ...stages.map((stage) => stage.contextTokens ?? 0));
}

export const totalCost = (stages) => stages.reduce((sum, stage) => sum + (stage.cost ?? 0), 0);

/**
 * Цена этапа. Токены, восстановленные из потока убитого этапа, — оценка снизу,
 * а не показание драйвера; цену, которую драйвер не назвал, не выдумываем.
 */
export function formatCost(stage) {
  return stage.usageApprox || stage.cost == null ? '$?' : `$${stage.cost.toFixed(2)}`;
}

/**
 * Строка отчёта об этапе. Одна на оба вывода — таблицу лога и markdown:
 * порознь они разъезжались бы, а показывают одно и то же.
 */
function stageRow(stage) {
  return {
    name: stage.ok ? stage.name : `${stage.name} (${stage.reason ?? 'не выполнен'})`,
    model: stage.model ?? '',
    effort: stage.effort ?? '',
    turns: String(stage.turns ?? 0),
    tokens: `${stage.usageApprox ? '≈' : ''}${tokensOf(stage.usage).toLocaleString('ru-RU')}`,
    context: (stage.contextTokens ?? 0).toLocaleString('ru-RU'),
    cost: formatCost(stage),
  };
}

export function formatCostTable(stages, { elapsedMs }) {
  const rows = [['Этап', 'Модель', 'Усилие', 'Время', 'Ходы', 'Токены', 'Размер контекста', 'Цена']];
  for (const stage of stages) {
    const row = stageRow(stage);
    rows.push([
      row.name, row.model, row.effort, `${Math.round(stage.durationMs / 1000)}с`,
      row.turns, row.tokens, row.context, row.cost,
    ]);
  }
  const totalTokens = stages.reduce((sum, stage) => sum + tokensOf(stage.usage), 0);
  const totalTurns = stages.reduce((sum, stage) => sum + (stage.turns ?? 0), 0);
  const context = peakContextTokens(stages);
  const worked = stages.reduce((sum, stage) => sum + stage.durationMs, 0);
  const unknown = stages.some((stage) => stage.cost == null) ? ' + $?' : '';
  rows.push([
    'ИТОГО', '', '', `${Math.round(elapsedMs / 1000)}с`, String(totalTurns),
    totalTokens.toLocaleString('ru-RU'), context.toLocaleString('ru-RU'), `$${totalCost(stages).toFixed(2)}${unknown}`,
  ]);
  if (worked > elapsedMs) {
    rows.push(['  из них параллельно', '', '', `${Math.round((worked - elapsedMs) / 1000)}с`, '', '', '', '']);
  }

  const widths = rows[0].map((_, column) => Math.max(...rows.map((row) => row[column].length)));
  return rows.map((row) => row.map((cell, i) => cell.padEnd(widths[i])).join('  ')).join('\n');
}

function formatDuration(ms) {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds} с`;
  return `${Math.floor(seconds / 60)} мин ${seconds % 60} с`;
}

/** Токены и время по этапам — в статус MR: по ним видно, что разбор не был пустым. */
export function formatRunStats(stages, { elapsedMs }) {
  const totalTokens = stages.reduce((sum, stage) => sum + tokensOf(stage.usage), 0);
  const approx = stages.some((stage) => stage.usageApprox) ? '≈' : '';
  const rows = stages.map((stage) => {
    const row = stageRow(stage);
    return `| ${row.name} | ${row.model} | ${row.effort} | ${formatDuration(stage.durationMs)}`
      + ` | ${row.turns} | ${row.tokens} | ${row.context} |`;
  });

  return [
    `Разбор занял ${formatDuration(elapsedMs)}, потрачено ${approx}${totalTokens.toLocaleString('ru-RU')} токенов.`,
    '',
    '<details><summary>По этапам</summary>',
    '',
    '| Этап | Модель | Усилие | Время | Ходы | Токены | Размер контекста |',
    '|---|---|---|---|---|---|---|',
    ...rows,
    '',
    '</details>',
  ].join('\n');
}

export function costSummary(stages) {
  return {
    usd: totalCost(stages),
    usdKnown: !stages.some((stage) => stage.cost == null || stage.usageApprox),
    turns: stages.reduce((sum, stage) => sum + (stage.turns ?? 0), 0),
    tokens: stages.reduce((sum, stage) => sum + tokensOf(stage.usage), 0),
    contextTokens: peakContextTokens(stages),
    durationMs: stages.reduce((sum, stage) => sum + stage.durationMs, 0),
  };
}

export function writeCostJson({ path, stages }) {
  writeFileSync(path, `${JSON.stringify({
    stages: stages.map((stage) => ({
      name: stage.name,
      model: stage.model,
      effort: stage.effort ?? null,
      durationMs: stage.durationMs,
      cost: stage.cost,
      turns: stage.turns ?? 0,
      tokens: tokensOf(stage.usage),
      contextTokens: stage.contextTokens ?? 0,
      // usage запросов как в журнале сессии codex: вход, кэш, выход, reasoning
      ...(stage.requests ? { requests: stage.requests } : {}),
      ok: stage.ok,
    })),
    total: costSummary(stages),
  }, null, 2)}\n`, 'utf-8');
}
