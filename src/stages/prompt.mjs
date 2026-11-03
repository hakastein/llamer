export function bullets(items) {
  return items.map((item) => `- ${item}`).join('\n');
}

function indent(value) {
  return String(value).replace(/\n/g, '\n  ');
}

function formatFinding(finding) {
  const head = `- #${finding.id} [${finding.severity}] (этап ${finding.stage}) `
    + `${finding.file}:${finding.line}-${finding.lineEnd} — ${indent(finding.text)}`;
  const extra = [];
  if (finding.severityReason) extra.push(`  Обоснование веса: ${indent(finding.severityReason)}`);
  if (finding.suggestion) extra.push(`  Предложенная правка: ${indent(finding.suggestion)}`);
  if (finding.context) extra.push(`  Код вокруг находки:\n${indent(finding.context)}`);
  if (finding.trace) extra.push(`  След: ${indent(finding.trace)}`);
  if (Array.isArray(finding.relatedContext) && finding.relatedContext.length > 0) {
    const blocks = finding.relatedContext.map((related) => (related.snippet
      ? `  ${related.file}:\n${indent(indent(related.snippet))}`
      : `  ${related.file} (сюда смотрел автор находки)`));
    extra.push(`  Связанный код:\n${blocks.join('\n')}`);
  }
  return [head, ...extra].join('\n');
}

export function formatFindings(findings) {
  if (findings.length === 0) return '- находок пока нет';
  return findings.map(formatFinding).join('\n');
}

/** Разбирающие этапы прогона с их мандатами — служебным этапам, судящим их находки. */
export function formatStages(stages) {
  return stages.map((stage) => `- ${stage.id}: ${stage.mandate.replace(/\s+/g, ' ')}`).join('\n');
}
