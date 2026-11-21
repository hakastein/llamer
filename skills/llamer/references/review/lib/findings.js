// Живые находки для задания этапов дедупликации и скептика.
"use strict";
const {at} = require("./state.js");

// pad — строк кода над находкой и под ней.
const pad = 3;

const indent = (text) => text.replaceAll("\n", "\n  ");

function window(lines, start, end) {
  const out = [];
  for (let i = Math.max(1, start - pad); i <= Math.min(lines.length, end + pad); i++) {
    out.push("L" + i + "\t" + lines[i - 1]);
  }
  return out.join("\n");
}

const listed = (items) => items.length > 1 ? items.slice(0, -1).join(", ") + " and " + items.at(-1) : items.join("");

// listing — находки, не помеченные повтором и не опровергнутые, со следом автора; с code — и с кодом вокруг каждой и
// кодом, на который она опирается. s — состояние ревью, r — его репозиторий.
exports.listing = (s, r, code) => {
  const live = s.findings.filter((f) => !f.duplicateOf && !f.refuted);
  if (live.length === 0) {
    return "- no findings yet";
  }
  const stages = listed([...new Set(s.findings.map((f) => f.stage))]);
  return "The findings were written by the stages " + stages + ".\n\n" + live.map((f) => {
    const lines = [`- #${f.id} [${f.severity}] ${at(f)} — ${indent(f.text)}`];
    if (f.severityReason) {
      lines.push("  Severity rationale: " + indent(f.severityReason));
    }
    if (f.suggestion) {
      lines.push("  Suggested fix: " + indent(f.suggestion));
    }
    // Находка на удалённый код — без окна: её строки из старой версии, и вырезка из файла подсунула бы чужой код.
    const file = code && !f.deleted ? r.lines(f.file) : null;
    if (file) {
      lines.push("  Code around the finding:\n" + indent(window(file, f.line, f.lineEnd)));
    }
    if (f.trace) {
      lines.push("  Trail: " + indent(f.trace));
    }
    if (code && f.related.length > 0) {
      lines.push("  Related code:\n" + f.related.map((rel) => {
        const text = rel.line ? r.lines(rel.file) : null;
        return text ? "  " + rel.file + ":\n" + indent(indent(window(text, rel.line, rel.lineEnd)))
          : "  " + rel.file + " (the reviewer looked here)";
      }).join("\n"));
    }
    return lines.join("\n");
  }).join("\n");
};
