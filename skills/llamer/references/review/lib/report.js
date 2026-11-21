// Итог ревью из журнала: что дойдёт до автора и чего ревью не покрыло. Его читают отчёт и публикация.
"use strict";
const {at} = require("./state.js");
const {yaml} = require("./yaml.js");

// findings — выжившие находки без дословных повторов: этапы пишут независимо, а повторить запись агент может и
// сам. Разные замечания на одной строке остаются оба.
exports.findings = (s) => {
  const seen = new Set();
  return s.surviving().filter((f) => {
    const key = at(f) + "\n" + f.text;
    return !seen.has(key) && seen.add(key);
  });
};

// gaps — чего ревью не покрыло: прекращённые этапы, находки без вердикта, нет оценки, неопубликованное.
exports.gaps = (s, unpublished = []) => {
  const gaps = s.stops.map((x) => `этап «${x.stage}» прекратил работу: ${x.reason}`);
  const waiting = s.findings.filter((f) => !f.verdicted && !f.duplicateOf).map((f) => "#" + f.id);
  if (waiting.length > 0) {
    gaps.push(`скептик не проверил находок: ${waiting.length} (${waiting.join(", ")})`);
  }
  if (!s.assessment) {
    gaps.push("вердикта оценщика в журнале нет");
  }
  if (unpublished.length > 0) {
    gaps.push(`не опубликовано замечаний: ${unpublished.length} (${unpublished.map(at).join(", ")})`);
  }
  return gaps;
};

// report — итог ревью s одним документом YAML: оценка, находки, что дойдут до автора, сводки этапов, треды к
// закрытию и пробелы.
exports.report = (s) => {
  const a = s.assessment;
  return yaml({
    base: s.materials.base,
    assessment: a && {nature: a.nature, risk: a.risk, level: a.level, reason: a.reason},
    findings: exports.findings(s).map((f) => ({id: f.id, stage: f.stage, file: f.file, line: f.line, lineEnd: f.lineEnd,
      deleted: f.deleted, severity: f.severity, severityReason: f.severityReason, text: f.text,
      suggestion: f.suggestion, related: f.related, trace: f.trace})),
    summaries: s.summaries,
    resolvedThreads: s.resolved.map((t) => ({id: t.id, reason: t.reason, place: t.place})),
    gaps: exports.gaps(s),
  });
};
