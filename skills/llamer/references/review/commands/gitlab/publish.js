const repo = require("../lib/repo.js");
const gitlab = require("../lib/gitlab.js");
const {taskDiff} = require("../lib/task.js");
const {state, at} = require("../lib/state.js");
const {findings} = require("../lib/report.js");
const {escapeDollarsOutsideCode: escape, withRevisionMark, statusMarker} = require("../lib/markdown.js");

exports.definition = {
  short: "Publish the review to a merge request",
  long: "Publish the review to a GitLab merge request: close the threads the threads stage settled, post every " +
    "finding as a thread on its lines (a finding off the request's lines goes as a note), the stage summaries and " +
    "the status note. With --dry-run prints the write requests as JSON lines instead of sending them.",
  flags: [
    {name: "mr", type: "string", usage: "the merge request URL"},
    {name: "token", type: "string", usage: "a GitLab token with the api scope"},
    {name: "dry-run", type: "bool", usage: "send nothing, print the write requests"},
  ],
};

// plural — форма русского слова по числу: замечание, замечания, замечаний.
function plural(n, one, few, many) {
  const [mod10, mod100] = [n % 10, n % 100];
  return mod100 >= 11 && mod100 <= 14 ? many : mod10 === 1 ? one : mod10 >= 2 && mod10 <= 4 ? few : many;
}

// withSuggestion — правка, применяемая кнопкой. Смещения в `suggestion:-N+M` GitLab считает от строки, на которой
// стоит комментарий, а у многострочного треда это последняя строка диапазона: N = длина диапазона минус один, M = 0.
function withSuggestion(text, f) {
  const longest = Math.max(0, ...(f.suggestion.match(/`+/g) ?? []).map((run) => run.length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  // Пустая правка — блок без содержимого: кнопка удаляет выделенные строки.
  const content = f.suggestion ? f.suggestion + "\n" : "";
  return text + "\n\n" + fence + "suggestion:-" + (f.lineEnd - f.line) + "+0\n" + content + fence;
}

// findingBody — тело инлайн-треда. Экранируется только текст замечания: блок suggestion GitLab читает дословно.
function findingBody(f, head) {
  const text = escape(f.text);
  return withRevisionMark(f.suggestion === null ? text : withSuggestion(text, f), head);
}

// unanchoredBody — замечание, которое не встало на строки: место переезжает в текст, правка — обычным блоком кода.
function unanchoredBody(f, head) {
  const place = "**`" + f.file + ":" + f.line + (f.lineEnd !== f.line ? "-" + f.lineEnd : "") + "`**" +
    (f.deleted ? " (удалённые строки)" : "");
  const parts = [place + " — замечание не встало на строки реквеста", escape(f.text)];
  if (f.suggestion !== null) {
    parts.push("Предлагаемая замена:\n\n```\n" + f.suggestion + "\n```");
  }
  return withRevisionMark(parts.join("\n\n"), head);
}

exports.command = (flags) => {
  const s = state();
  const dryRun = flags["dry-run"] === true;
  const mr = gitlab.open(flags.mr, flags.token, dryRun);
  const head = mr.info().diff_refs.head_sha;
  const log = [];
  const failed = [];
  const attempt = (label, call) => {
    try {
      call();
      return true;
    } catch (e) {
      failed.push(label + ": " + e.message);
      return false;
    }
  };
  for (const t of s.resolved) {
    attempt("тред " + t.place + " закрыть не удалось", () => {
      mr.resolve(t.id);
      log.push("закрыт тред " + t.place + " — " + t.reason);
    });
  }
  // Новая сторона у изменений задачи и merge request одна — голова, а старая у каждого своя: удалённые строки
  // переносятся на нумерацию merge request.
  let version, request, task;
  const position = (f) => {
    version ??= mr.version();
    request ??= mr.diff();
    task ??= taskDiff(repo.open(), s.materials.base).parsed;
    const inRequest = (line) => f.deleted ? gitlab.deletedLineInRequest(f.file, line, task, request) : line;
    return gitlab.position(f.file, inRequest(f.line), inRequest(f.lineEnd), f.deleted, request, version);
  };
  const postUnanchored = (f) => attempt("замечание " + at(f) + " опубликовать не удалось", () => {
    mr.postDiscussion(unanchoredBody(f, head));
    log.push("опубликовано замечание " + at(f) + ", без привязки к строкам");
  });
  // Замечание, которое не встало на строки, всё равно доезжает до автора — заметкой с местом в тексте.
  const post = (f) => {
    let p;
    try {
      p = position(f);
    } catch (e) {
      log.push(at(f) + ": " + e.message + " — публикуем заметкой");
      return postUnanchored(f);
    }
    let thread;
    if (!attempt("тред " + at(f) + " опубликовать не удалось", () => {
      thread = mr.postDiscussion(findingBody(f, head), p);
    })) {
      return false;
    }
    if (!dryRun && !gitlab.anchored(thread)) {
      // Нота уже висит в merge request без места и с неприменимой правкой: её сносим и повторяем заметкой.
      log.push(at(f) + ": GitLab не принял позицию — заменяем заметкой");
      attempt("непривязанную ноту " + at(f) + " убрать не удалось", () => mr.deleteNote(thread.notes[0].id));
      return postUnanchored(f);
    }
    log.push("опубликован тред " + at(f));
    return true;
  };
  const unpublished = findings(s).filter((f) => !post(f));
  const author = unpublished.length > 0 ? [`не опубликовано замечаний: ${unpublished.length} (${unpublished.map(at).join(", ")})`] : [];
  const summary = [...s.summaries.filter((x) => x.text.trim()).map((x) => "**" + x.stage + "**: " + x.text.trim()),
    ...(author.length > 0 ? ["### Ревью неполное\n\n" + author.map((g) => "- " + g).join("\n")] : [])].join("\n\n");
  if (summary) {
    attempt("сводку опубликовать не удалось", () => {
      mr.postNote(escape(withRevisionMark(summary, head)));
      log.push("опубликована сводка");
    });
  }
  // Метка статуса — точка отсчёта следующего прогона: с недостачей она дала бы ему пропустить неразобранное.
  const n = unpublished.length;
  const status = n > 0 ? `_Ревью выполнено, ${n} ${plural(n, "замечание", "замечания", "замечаний")} опубликовать не удалось — см. сводку._`
    : "_Ревью выполнено._";
  const mark = n === 0 && s.stops.length === 0 ? head : "";
  attempt("статус опубликовать не удалось", () => mr.postNote(escape(withRevisionMark(statusMarker + "\n" + status, mark))));
  if (failed.length > 0) {
    throw new Error(failed.join("\n"));
  }
  return dryRun ? mr.planned.map((r) => JSON.stringify(r)).join("\n") : log.join("\n");
};
