// Треды merge request, привязанные к строкам: чьи они, открыты ли, что в них сказано.
"use strict";
const {hasRevisionMark} = require("./markdown.js");

const excerpt = 400;
const machineRevision = /<sub>ревизия[\s\S]*$/i;
const htmlComment = /<!--[\s\S]*?-->/g;

const withoutMachineMarks = (body) => body.replace(machineRevision, "").replace(htmlComment, "").trim();

function threadText(body) {
  const text = [...withoutMachineMarks(body).split(/\s+/).filter(Boolean).join(" ")];
  return text.length > excerpt ? text.slice(0, excerpt).join("") + "…" : text.join("");
}

// summarise — треды из discussions GitLab. Наша анкерная нота опознаётся по метке ревизии: имя бота зависит от
// токена публикации. Ответы по метке опознавать нельзя — «Quote reply» вставляет цитату вместе с меткой.
exports.summarise = (discussions) => discussions.flatMap((d) => {
  const notes = d.notes.filter((n) => !n.system);
  const anchor = notes.findIndex((n) => n.position?.position_type === "text");
  if (anchor < 0) {
    return [];
  }
  const a = notes[anchor];
  const p = a.position;
  const t = {id: d.id, ours: hasRevisionMark(a.body), resolved: notes.some((n) => n.resolved), file: p.new_path || p.old_path,
    line: [p.line_range?.end?.new_line, p.new_line, p.old_line].find((n) => n) ?? 0};
  const replies = notes.filter((n, i) => i !== anchor && !(t.ours && n.author.username === a.author.username))
    .map((n) => threadText(n.body)).filter(Boolean);
  t.state = t.ours ? (t.resolved ? "наш, закрыт" : "наш, открыт") : "человек";
  // Тред с пустым телом анкера остаётся в списке: по строке уже есть разговор.
  t.text = threadText(a.body) || "(заметка без текста: картинка или предложенная правка)";
  t.reply = replies.length > 0 ? replies.join(" ") : null;
  return [t];
});

const rules = "An open thread of ours means the remark already hangs there; there is no need to repeat it. A closed" +
  " thread of ours means the question is settled; it may not be raised again in any wording, and who closed it does" +
  " not matter. A human's thread — do not duplicate it.";

// format — что уже сказано в merge request: правила и треды.
exports.format = (threads) => threads.length === 0 ? "" : rules + "\n\n" + threads.flatMap((t) =>
  [`- ${t.file}:${t.line} [${t.state}] — ${t.text}`, ...(t.reply ? ["  author's reply: " + t.reply] : [])]).join("\n");

// open — наши открытые треды: их сверяет этап тредов.
exports.open = (threads) => threads.filter((t) => t.ours && !t.resolved);

// openList — наши открытые треды для задания этапа тредов.
exports.openList = (threads) => exports.open(threads).map((t) => `- id ${t.id} — ${t.file}:${t.line}\n  ${t.text}` +
  (t.reply ? "\n  author's reply: " + t.reply : "")).join("\n") || "- there are no open threads of ours";
