// Итог ревью из журнала: что дойдёт до автора и чего ревью не покрыло. Его читают отчёт и публикация.
"use strict";
const {at} = require("./state.js");

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
