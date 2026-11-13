import { join } from 'node:path';
import { stringify } from 'yaml';
import { formatCostTable, costSummary } from '../agent/cost.mjs';
import { driverLimits } from '../agent/limits.mjs';
import { formatFailureComment } from './failure.mjs';
import { buildScopeBlock } from './scope.mjs';
import { nothingOwnText } from './ReviewReport.mjs';
import { redactSecrets } from '../redact.mjs';

export const REPORT_VERSION = 3;

export const REPORT_FILE = 'report.md';

export const RESULT_FILE = 'result.yaml';

/** YAML для агента: многострочный текст — блоками `|`, длинные строки не переносятся. */
export function toYaml(value) {
  return stringify(value, { blockQuote: 'literal', lineWidth: 0 });
}

// Что этап стоил и как продолжить его сессию: команда, к которой дописывают вопрос в кавычках.
const stageEntry = (stage) => ({
  name: stage.name, model: stage.model, effort: stage.effort ?? null, ok: stage.ok, usd: stage.cost ?? null, resume: stage.resume ?? null,
});

// Цена, лимиты и этапы — в каждом результате ревью, даже если этапы не поднимались.
const stageFields = (stages) => ({
  cost: costSummary(stages), limits: driverLimits(stages), stages: stages.map(stageEntry),
});

function findingBlock(finding, index) {
  const lines = [`### ${index + 1}. \`${finding.at}\` [${finding.severity}] — этап ${finding.stage ?? '?'}`, '', finding.text];
  if (finding.severityReason) lines.push('', `Вес: ${finding.severityReason}`);
  if (finding.suggestion != null) lines.push('', 'Предлагаемая замена:', '', '```', finding.suggestion, '```');
  if (finding.trace) lines.push('', `След: ${finding.trace}`);
  return lines.join('\n');
}

/**
 * Выход по умолчанию: YAML-результат в stdout и `result.yaml`, `report.md` из тех
 * же данных — в артефактах прогона. Результат читает запустивший прогон агент,
 * поэтому stdout несёт только его, а весь лог идёт в stderr.
 */
export class ReportDelivery {
  #request; #artifacts;

  constructor({ request, artifacts }) {
    this.#request = request;
    this.#artifacts = artifacts;
  }

  #emit(markdown, fields) {
    const report = this.#artifacts.saveText(REPORT_FILE, `${markdown}\n`);
    // Вымарывается до YAML: на месте секрета в тексте YAML звёздочки прочлись бы ссылкой.
    const result = toYaml(JSON.parse(redactSecrets(JSON.stringify({
      version: REPORT_VERSION,
      input: this.#request.kind,
      head: this.#request.headSha,
      ...fields,
      report,
      result: join(this.#artifacts.path, RESULT_FILE),
      artifacts: this.#artifacts.path,
    }))));
    this.#artifacts.saveText(RESULT_FILE, result);
    process.stdout.write(result);
    return report;
  }

  #title() {
    return `# Ревью ${this.#request.label}`;
  }

  async deliver(report) {
    const { findings, assessment } = report;
    const { gaps, notices } = report.shortfall([]);
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
      parts.push(`## Снятые треды\n\n${report.resolve.map((thread) => `- ${thread.place ?? thread.id} — ${thread.reason}`).join('\n')}`);
    }
    const summary = report.summary([]);
    if (summary) parts.push(`## Сводка\n\n${summary}`);
    if (gaps.length > 0) parts.push(`## Недостача\n\n${gaps.map((line) => `- ${line}`).join('\n')}`);
    const sessions = report.stages.filter((stage) => stage.resume);
    if (sessions.length > 0) {
      parts.push(`## Сессии этапов\n\nПродолжить сессию этапа вопросом к агенту:\n\n${sessions
        .map((stage) => `- ${stage.name}: \`${stage.resume} '<вопрос>'\``).join('\n')}`);
    }
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
      notices,
      ...stageFields(report.stages),
    });
    return { unpublished: [], failures: [] };
  }

  async deliverNothingOwn({ task }) {
    this.#emit([
      this.#title(),
      nothingOwnText(task),
      buildScopeBlock({ task }),
    ].join('\n\n'), {
      ok: true, base: task.baseSha, assessment: null, findings: [], summaries: [], resolvedThreads: [], gaps: [], notices: [],
      ...stageFields([]),
    });
    return { failures: [] };
  }

  // Сорванный прогон — тот случай, когда лимиты подписки интереснее всего: они в результате и здесь.
  async deliverFailure(failure, stages = []) {
    const text = formatFailureComment(failure);
    this.#emit(`${this.#title()}\n\n${text}`, {
      ok: false,
      failure: { reason: failure.reason, detail: failure.detail ?? null, text },
      findings: [],
      limits: driverLimits(stages),
    });
    return { failures: [] };
  }
}
