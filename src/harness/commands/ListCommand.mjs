import { Command } from '../Command.mjs';

function mark(finding) {
  if (finding.duplicateOf) return `повтор #${finding.duplicateOf}`;
  if (finding.refuted) return 'опровергнута';
  return finding.severity;
}

export class ListCommand extends Command {
  get name() {
    return 'list';
  }

  get summary() {
    return 'print the run journal: number, severity and place of every finding';
  }

  get manual() {
    return 'The whole journal, every stage, including findings already dropped from'
      + ' publication ([refuted], [duplicate of #N]). A write confirms without a number,'
      + ' so this is where a number comes from outside a batch report.';
  }

  get prints() {
    return true;
  }

  build(values, context) {
    const { findings, skippedLines } = context.state;
    const lines = findings.map((finding) => `${finding.id}. [${mark(finding)}] ${finding.at}`
      + ` — ${finding.text.replace(/\s+/g, ' ').slice(0, 120)}`);
    if (lines.length === 0) lines.push('находок пока нет');
    if (skippedLines > 0) lines.push(`пропущено битых строк журнала: ${skippedLines}`);
    return { print: lines.join('\n') };
  }
}
