// Снятое дедупликатором и скептиком в реквест не едет — это единственный след,
// по которому видно, не перестарались ли они.
export function collectDropped(findings) {
  const lines = [];

  const duplicates = findings.filter((finding) => finding.duplicateOf);
  if (duplicates.length > 0) {
    lines.push(`дедупликация сняла находок: ${duplicates.length}`);
    for (const finding of duplicates) {
      lines.push(`  ${finding.file}:${finding.line} — повтор #${finding.duplicateOf}`
        + ` // ${finding.duplicateReason}`);
    }
  }

  const refuted = findings.filter((finding) => finding.refuted && !finding.duplicateOf);
  if (refuted.length > 0) {
    lines.push(`скептик опроверг находок: ${refuted.length}`);
    for (const finding of refuted) {
      lines.push(`  ${finding.file}:${finding.line} — ${finding.text} // ${finding.refuteReason}`);
    }
  }

  return lines;
}
