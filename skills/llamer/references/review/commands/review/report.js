const {state} = require("../lib/state.js");
const {findings, gaps} = require("../lib/report.js");
const {yaml} = require("../lib/yaml.js");

exports.definition = {
  short: "Print the review result as YAML",
  long: "Print the review result as one YAML document: the assessment, the findings that reach the author, the " +
    "stage summaries, the threads to close and the gaps — what the review did not cover. Meant for the last " +
    "command stage of the workflow.",
};

exports.command = () => {
  const s = state();
  const a = s.assessment;
  return yaml({
    base: s.materials.base,
    assessment: a && {nature: a.nature, risk: a.risk, level: a.level, reason: a.reason},
    findings: findings(s).map((f) => ({id: f.id, stage: f.stage, file: f.file, line: f.line, lineEnd: f.lineEnd,
      deleted: f.deleted, severity: f.severity, severityReason: f.severityReason, text: f.text,
      suggestion: f.suggestion, related: f.related, trace: f.trace})),
    summaries: s.summaries,
    resolvedThreads: s.resolved.map((t) => ({id: t.id, reason: t.reason, place: t.place})),
    gaps: gaps(s),
  });
};
