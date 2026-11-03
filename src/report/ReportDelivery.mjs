import { formatCostTable, costSummary } from '../agent/cost.mjs';
import { formatFailureComment } from './failure.mjs';
import { buildScopeBlock } from './scope.mjs';
import { redactSecrets } from '../redact.mjs';

export const REPORT_VERSION = 1;

export const REPORT_FILE = 'report.md';

function findingBlock(finding, index) {
  const lines = [`### ${index + 1}. \`${finding.at}\` [${finding.severity}] — этап ${finding.stage ?? '?'}`, '', finding.text];
  if (finding.severityReason) lines.push('', `Вес: ${finding.severityReason}`);
  if (finding.suggestion != null) lines.push('', 'Предлагаемая замена:', '', '```', finding.suggestion, '```');
  if (finding.trace) lines.push('', `След: ${finding.trace}`);
  return lines.join('\n');
}

/**
 * Выход по умолчанию: `report.md` в артефактах прогона и JSON находок в stdout.
 * JSON читает запустивший прогон агент, поэтому stdout несёт только его, а весь
 * лог идёт в stderr.
 */
export class ReportDelivery {
  #request; #artifacts;

  constructor({ request, artifacts }) {
    this.#request = request;
    this.#artifacts = artifacts;
  }

  #emit(markdown, fields) {
    const report = this.#artifacts.saveText(REPORT_FILE, `${markdown}\n`);
    process.stdout.write(`${redactSecrets(JSON.stringify({
      version: REPORT_VERSION,
      input: this.#request.kind,
      head: this.#request.headSha,
      ...fields,
      report,
      artifacts: this.#artifacts.path,
    }, null, 2))}\n`);
    return report;
  }

  #title() {
    return `# Ревью ${this.#request.label}`;
  }

  async deliver(report) {
    const { findings, assessment } = report;
    const gaps = report.shortfall([]).gaps;
    const parts = [
      this.#title(),
      `База \`${report.baseSha.slice(0, 8)}\`, голова \`${this.#request.headSha.slice(0, 8)}\`.`
        + ` Артефакты прогона: ${this.#artifacts.path}`,
    ];
    if (report.riskNote) parts.push(`## Заметка о рисках\n\n${report.riskNote}`);
    parts.push(findings.length > 0
      ? `## Находки: ${findings.length}\n\n${findings.map(findingBlock).join('\n\n')}`
      : '## Находок нет');
    if (report.resolve.length > 0) {
      parts.push(`## Снятые треды\n\n${report.resolve.map((thread) => `- ${thread.at ?? thread.id} — ${thread.reason}`).join('\n')}`);
    }
    const summary = report.summary([]);
    if (summary) parts.push(`## Сводка\n\n${summary}`);
    if (gaps.length > 0) parts.push(`## Недостача\n\n${gaps.map((line) => `- ${line}`).join('\n')}`);
    if (report.stages.length > 0) {
      parts.push(`## Цена\n\n\`\`\`\n${formatCostTable(report.stages, { elapsedMs: report.elapsedMs })}\n\`\`\``);
    }
    this.#emit(parts.join('\n\n'), {
      ok: true,
      base: report.baseSha,
      assessment: assessment && {
        nature: assessment.nature, risk: assessment.risk, level: assessment.level, reason: assessment.reason,
      },
      findings: findings.map((finding) => finding.toJSON()),
      summaries: report.summaries,
      resolvedThreads: report.resolve,
      gaps,
      cost: costSummary(report.stages),
    });
    return { unpublished: [], failures: [] };
  }

  async deliverNothingOwn({ task }) {
    this.#emit([
      this.#title(),
      'Своих изменений нет: всё уже есть в целевой и общих ветках. Ревью пропущено.',
      buildScopeBlock({ task }),
    ].join('\n\n'), {
      ok: true, base: task.baseSha, assessment: null, findings: [], summaries: [], resolvedThreads: [], gaps: [],
    });
    return { failures: [] };
  }

  async deliverFailure(failure) {
    const text = formatFailureComment(failure);
    this.#emit(`${this.#title()}\n\n${text}`, {
      ok: false, failure: { reason: failure.reason, detail: failure.detail ?? null, text }, findings: [],
    });
    return { failures: [] };
  }
}
