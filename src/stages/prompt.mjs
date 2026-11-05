import { tagged } from '../materials/ContextPack.mjs';

export function bullets(items) {
  return items.map((item) => `- ${item}`).join('\n');
}

export function packBlock(text, whenEmpty = '') {
  return tagged('added_comments', text || whenEmpty);
}

function indent(value) {
  return String(value).replace(/\n/g, '\n  ');
}

function formatFinding(finding) {
  const head = `- #${finding.id} [${finding.severity}] `
    + `${finding.file}:${finding.line}-${finding.lineEnd} — ${indent(finding.text)}`;
  const extra = [];
  if (finding.severityReason) extra.push(`  Severity rationale: ${indent(finding.severityReason)}`);
  if (finding.suggestion) extra.push(`  Suggested fix: ${indent(finding.suggestion)}`);
  if (finding.context) extra.push(`  Code around the finding:\n${indent(finding.context)}`);
  if (finding.trace) extra.push(`  Trail: ${indent(finding.trace)}`);
  if (Array.isArray(finding.relatedContext) && finding.relatedContext.length > 0) {
    const blocks = finding.relatedContext.map((related) => (related.snippet
      ? `  ${related.file}:\n${indent(indent(related.snippet))}`
      : `  ${related.file} (the reviewer looked here)`));
    extra.push(`  Related code:\n${blocks.join('\n')}`);
  }
  return [head, ...extra].join('\n');
}

export function formatFindings(findings) {
  if (findings.length === 0) return '- no findings yet';
  return findings.map(formatFinding).join('\n');
}

/** «a, b and c» — перечисление в тексте задания. */
export function listed(items) {
  return items.length > 1 ? `${items.slice(0, -1).join(', ')} and ${items.at(-1)}` : items.join('');
}

/** Разбирающие этапы прогона одной фразой — служебным этапам, судящим их находки. */
export function formatStages(stages) {
  return listed(stages.map((stage) => stage.title));
}
