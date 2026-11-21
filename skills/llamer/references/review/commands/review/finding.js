const llamer = require("llamer");
const repo = require("../../lib/repo.js");
const {taskDiff} = require("../../lib/task.js");
const {state, at} = require("../../lib/state.js");

exports.definition = {
  short: "Record a remark on the change, anchored to diff lines",
  long: "Record a remark on the change, anchored to diff lines. Highlight everything the remark is about — the " +
    "whole condition, call, branch or method; that is where the thread anchors.\n" +
    "For DELETED code use --deleted, anchored on the old side; otherwise the thread lands on the new side, on " +
    "whatever replaced it.",
  flags: [
    {name: "file", type: "string", usage: "a repo-root-relative path, as in the diff"},
    {name: "line", type: "int", usage: "first line of the fragment"},
    {name: "line-end", type: "int", usage: "last line of it; no default, so repeat --line for a one-line remark"},
    {name: "text", type: "string", usage: "what is wrong and how to solve it"},
    {name: "severity", type: "string", choices: ["minor", "major", "critical"], usage: "the level the consequence puts the finding on"},
    {name: "severity-reason", type: "string", usage: "the one consequence that puts it there, distinct from --text"},
    {name: "related", type: "string", usage: "what the remark rests on: «path», «path:line» or «path:line-line», " +
      "comma-separated; anything else in the value is dropped silently. Lines are from the new version even with --deleted"},
    {name: "trace", type: "string", usage: "the chain of reasoning, for a finding about interaction"},
    {name: "suggestion", type: "string", usage: "replaces the WHOLE fragment, any length; the button applies it " +
      "unread, so offer one only when you know the whole fix. Markup is added for you"},
    {name: "deleted", type: "bool", usage: "the fragment is deleted lines: --line/--line-end are from the version " +
      "BEFORE the change (git show <base>:<file>)"},
  ],
};

// maxSuggestionSpan — правку длиннее объясняют текстом, а не кнопкой применения.
const maxSuggestionSpan = 20;

const relatedItem = /^([^:]+)(?::(\d+)(?:-(\d+))?)?$/;

// related — места «путь», «путь:строка», «путь:строка-строка» через запятую; прочее отбрасывается.
function related(text) {
  return (text ?? "").split(",").flatMap((item) => {
    const m = relatedItem.exec(item.trim());
    const file = m?.[1].trim();
    if (!m || file.includes("..") || file.startsWith("/")) {
      return [];
    }
    const line = m[2] ? Number(m[2]) : null;
    return [{file, line, lineEnd: m[3] ? Number(m[3]) : line}];
  });
}

exports.command = (flags) => {
  const f = {type: "finding", file: flags.file, line: flags.line, lineEnd: flags["line-end"], deleted: flags.deleted === true,
    text: flags.text, severity: flags.severity, severityReason: flags["severity-reason"],
    suggestion: flags.suggestion ?? null, related: related(flags.related), trace: flags.trace || null};
  if (!f.file || !f.line || !f.lineEnd || !f.text || !f.severity || !f.severityReason) {
    throw new Error("нужны --file, --line, --line-end, --text, --severity и --severity-reason");
  }
  if (f.related.length === 0) {
    throw new Error("нужен --related: куда ты смотрел, устанавливая находку («путь:строка» через запятую). " +
      "Если находка очевидна по одной строке, назови эту строку.");
  }
  const r = repo.open();
  assertAnchored(f, taskDiff(r, state(llamer.data).materials.base).parsed);
  if (f.suggestion !== null) {
    assertSuggestionApplies(f, r.lines(f.file) ?? []);
  }
  llamer.record(f);
  return "записано: finding " + at(f);
};

function assertAnchored(f, diff) {
  if (!diff.touches(f.file, f.deleted)) {
    throw new Error(f.deleted ? `в файле ${f.file} эти изменения строк не удаляли — --deleted не к чему привязать`
      : `файла ${f.file} нет среди изменений задачи`);
  }
  for (const number of [f.line, f.lineEnd]) {
    if (!diff.hasLine(f.file, number, f.deleted)) {
      const [state, kind] = f.deleted ? ["не удалена", "Удалённые"] : ["не изменена", "Изменённые"];
      throw new Error(`строка ${number} в ${f.file} ${state} этими изменениями — тред к ней не привязать. ` +
        `${kind} строки: ${diff.linesOf(f.file, f.deleted).join(", ")}`);
    }
  }
  if (f.lineEnd < f.line) {
    throw new Error("--line-end меньше --line");
  }
}

function assertSuggestionApplies(f, lines) {
  // Правка кнопкой заменяет строки новой версии; у удалённой строки их нет.
  if (f.deleted) {
    throw new Error("--suggestion не привязать к удалённой строке: заменять нечего, опиши правку в --text");
  }
  const span = f.lineEnd - f.line + 1;
  if (span > maxSuggestionSpan) {
    throw new Error(`правка на ${span} строк слишком велика: предел ${maxSuggestionSpan}. ` +
      "Правку такого размера объясняют текстом, а не кнопкой применения");
  }
  if (f.lineEnd > lines.length) {
    throw new Error(`строка ${f.lineEnd} за концом ${f.file} (в нём ${lines.length} строк)`);
  }
  if (lines.slice(f.line - 1, f.lineEnd).join("\n") === f.suggestion) {
    throw new Error("правка совпадает с текущим кодом — заменять нечего. Проверь --line и --line-end");
  }
}
