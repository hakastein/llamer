// Состояние ревью — свёртка записей журнала прогона по порядку записи. Номер находки — её место среди
// находок журнала: он выдаётся этапам и не меняется.
"use strict";
const llamer = require("llamer");

// at — место находки: «путь:строка-строка».
exports.at = (f) => f.file + ":" + f.line + "-" + f.lineEnd;

exports.state = () => {
  const s = {materials: null, threads: [], assessment: null, findings: [], summaries: [], resolved: [], stops: []};
  const byId = (id) => s.findings[id - 1];
  for (const {stage, value} of llamer.results()) {
    switch (value.type) {
      case "materials":
        s.materials = value;
        break;
      case "threads":
        s.threads = value.threads;
        break;
      case "assess":
        s.assessment = value;
        break;
      case "finding":
        s.findings.push({...value, id: s.findings.length + 1, stage, verdicted: false, refuted: false, duplicateOf: 0});
        break;
      case "dupe": {
        const f = byId(value.id);
        if (f && byId(value.of) && value.id !== value.of) {
          [f.duplicateOf, f.duplicateReason] = [value.of, value.reason];
        }
        break;
      }
      case "verdict": {
        const f = byId(value.id);
        if (f) {
          [f.verdicted, f.refuted, f.refuteReason] = [true, value.refuted, value.reason];
          if (value.severity) {
            [f.severity, f.severityReason] = [value.severity, value.severityReason];
          }
        }
        break;
      }
      case "summary": {
        const i = s.summaries.findIndex((x) => x.stage === stage);
        s.summaries.splice(i < 0 ? s.summaries.length : i, 1, {stage, text: value.text});
        break;
      }
      case "resolve":
        if (!s.resolved.some((r) => r.id === value.id)) {
          s.resolved.push(value);
        }
        break;
      case "stop":
        s.stops.push({stage, reason: value.reason});
        break;
    }
  }
  if (!s.materials) {
    throw new Error("в журнале прогона нет материалов: до этапов ревью должен пройти этап-команда `review materials`");
  }
  s.byId = byId;
  s.surviving = () => s.findings.filter((f) => !f.refuted && !f.duplicateOf);
  return s;
};
