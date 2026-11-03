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
    return 'напечатать журнал прогона: номер, вес и место каждой находки';
  }

  get manual() {
    return 'Весь журнал, все этапы, включая находки, уже снятые с публикации'
      + ' ([опровергнута], [повтор #N]). Запись подтверждается без номера,'
      + ' так что вне ответа batch номер берётся отсюда.';
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
