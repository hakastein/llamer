// Merge request в GitLab: запросы к API через llamer.fetch, позиции тредов на строках диффа.
"use strict";
const llamer = require("llamer");
const {Diff} = require("./diff.js");

const mrURL = /^(https?:\/\/[^/]+)\/(.+?)\/-\/merge_requests\/(\d+)/;

// open — merge request по ссылке `https://gitlab.example.com/group/project/-/merge_requests/123`.
// dryRun — запросы на запись не уходят, а копятся в planned: чтение идёт как обычно.
exports.open = (url, token, dryRun) => {
  const m = mrURL.exec(url ?? "");
  if (!m) {
    throw new Error("не разобрать ссылку на merge request: " + url +
      "\nожидается https://gitlab.example.com/group/project/-/merge_requests/123");
  }
  if (!token) {
    throw new Error("нужен токен GitLab с правом api");
  }
  const base = m[1] + "/api/v4/projects/" + encodeURIComponent(m[2]) + "/merge_requests/" + m[3];
  const planned = [];
  const request = (method, path, body) => {
    if (dryRun && method !== "GET") {
      planned.push({method, path, body});
      return null;
    }
    const r = llamer.fetch(base + path, {method, headers: {"PRIVATE-TOKEN": token, "Content-Type": "application/json"},
      body: body === undefined ? "" : JSON.stringify(body)});
    if (r.status < 200 || r.status > 299) {
      throw new Error(`${method} ${path}: ${r.status} ${r.body.slice(0, 500)}`);
    }
    return r.body ? JSON.parse(r.body) : null;
  };
  return {
    dryRun,
    planned,
    info: () => request("GET", ""),
    // version — последняя версия диффа: позиции тредов строятся от неё.
    version() {
      const versions = request("GET", "/versions");
      if (!versions.length) {
        throw new Error("у merge request нет версий — публиковать треды не к чему");
      }
      return versions[0];
    },
    // diff — дифф самого GitLab; без access_raw_diffs часть файлов приезжает схлопнутой — с пустым diff.
    diff: () => mergeDiff(request("GET", "/changes?access_raw_diffs=true").changes),
    // discussions — все треды: GitLab отдаёт старые первыми, по одной странице свежих не видно.
    discussions() {
      const all = [];
      for (let page = 1; ; page++) {
        const chunk = request("GET", `/discussions?per_page=100&page=${page}`);
        all.push(...chunk);
        if (chunk.length < 100) {
          return all;
        }
      }
    },
    postDiscussion: (body, position) => request("POST", "/discussions", position ? {body, position} : {body}),
    deleteNote: (id) => request("DELETE", "/notes/" + id),
    resolve: (id) => request("PUT", "/discussions/" + id, {resolved: true}),
    postNote: (body) => request("POST", "/notes", {body}),
  };
};

// mergeDiff — дифф merge request из файлов /changes: у каждого — заголовки и ханки.
function mergeDiff(files) {
  const text = files.map((f) => ["diff --git a/" + f.old_path + " b/" + f.new_path,
    ...(f.new_file ? ["new file mode 100644"] : []), ...(f.deleted_file ? ["deleted file mode 100644"] : []),
    ...(f.renamed_file ? ["rename from " + f.old_path, "rename to " + f.new_path] : []),
    f.new_file ? "--- /dev/null" : "--- a/" + f.old_path, f.deleted_file ? "+++ /dev/null" : "+++ b/" + f.new_path,
    f.diff.replace(/\n$/, "")].join("\n")).join("\n");
  return new Diff(text);
}

// lineCode — код строки диффа в терминах GitLab: `sha1(путь)_<старая>_<новая>`.
const lineCode = (file, oldLine, newLine) => sha1(file) + "_" + oldLine + "_" + newLine;

// position — позиция инлайн-треда. Комментарий всегда несёт диапазон: GitLab ставит ноту на последнюю строку и
// подсвечивает от line_range.start до неё; диапазон в одну строку — частный случай той же разметки.
exports.position = (file, start, end, deleted, diff, version) => {
  for (const number of [start, end]) {
    if (!diff.hasLine(file, number, deleted)) {
      throw new Error(`строка ${file}:${number} не найдена среди ${deleted ? "удалённых" : "изменённых"} — код строки для GitLab не собрать`);
    }
  }
  const anchor = (number) => {
    const [oldLine, newLine] = diff.pairAt(file, number, deleted);
    return deleted ? {line_code: lineCode(file, oldLine, newLine), type: "old", old_line: number}
      : {line_code: lineCode(file, oldLine, newLine), type: "new", new_line: number};
  };
  return {position_type: "text", base_sha: version.base_commit_sha, head_sha: version.head_commit_sha,
    start_sha: version.start_commit_sha, new_path: file, old_path: diff.oldPathOf(file),
    line_range: {start: anchor(start), end: anchor(end)}, ...(deleted ? {old_line: end} : {new_line: end})};
};

// removedAt — удалённые строки файла, стоящие перед новой строкой newLine.
const removedAt = (diff, file, newLine) =>
  diff.linesOf(file, true).filter((n) => diff.line(file, n, true).other === newLine).sort((a, b) => a - b);

// deletedLineInRequest — удалённая строка в изменениях задачи пронумерована от их базы, а тред встаёт на старую
// сторону диффа merge request — от другой базы. В обоих диффах она стоит перед той же новой строкой и несёт тот же
// текст; блок одинаковых удалённых строк сопоставляется по порядку.
exports.deletedLineInRequest = (file, line, task, request) => {
  const entry = task.line(file, line, true);
  if (!entry) {
    throw new Error(`удалённой строки ${file}:${line} нет в изменениях задачи`);
  }
  const ours = removedAt(task, file, entry.other);
  const theirs = removedAt(request, file, entry.other);
  if (ours.length === theirs.length && ours.every((n, i) => task.line(file, n, true).text === request.line(file, theirs[i], true).text)) {
    return theirs[ours.indexOf(line)];
  }
  const matches = theirs.filter((n) => request.line(file, n, true).text === entry.text);
  if (matches.length === 1) {
    return matches[0];
  }
  throw new Error(`удалённая строка ${file}:${line} не найдена в диффе merge request`);
};

// anchored — GitLab не отказывает на непонятой позиции, а создаёт обычный тред к merge request целиком.
exports.anchored = (thread) => {
  const note = thread?.notes?.[0];
  return note?.type === "DiffNote" && note.position?.line_range != null;
};

function utf8(text) {
  const out = [];
  for (const ch of text) {
    const c = ch.codePointAt(0);
    if (c < 0x80) {
      out.push(c);
    } else if (c < 0x800) {
      out.push(0xc0 | c >> 6, 0x80 | c & 63);
    } else if (c < 0x10000) {
      out.push(0xe0 | c >> 12, 0x80 | c >> 6 & 63, 0x80 | c & 63);
    } else {
      out.push(0xf0 | c >> 18, 0x80 | c >> 12 & 63, 0x80 | c >> 6 & 63, 0x80 | c & 63);
    }
  }
  return out;
}

// sha1 — SHA-1 текста в UTF-8, hex: у скриптов команд нет криптографии, а GitLab адресует строку хешем пути.
function sha1(text) {
  const bytes = utf8(text);
  const bits = bytes.length * 8;
  bytes.push(0x80);
  while (bytes.length % 64 !== 56) {
    bytes.push(0);
  }
  bytes.push(0, 0, 0, 0, bits >>> 24 & 255, bits >>> 16 & 255, bits >>> 8 & 255, bits & 255);
  const h = [0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476, 0xc3d2e1f0];
  const w = new Array(80);
  for (let off = 0; off < bytes.length; off += 64) {
    for (let i = 0; i < 16; i++) {
      w[i] = bytes[off + 4 * i] << 24 | bytes[off + 4 * i + 1] << 16 | bytes[off + 4 * i + 2] << 8 | bytes[off + 4 * i + 3];
    }
    for (let i = 16; i < 80; i++) {
      const x = w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16];
      w[i] = x << 1 | x >>> 31;
    }
    let [a, b, c, d, e] = h;
    for (let i = 0; i < 80; i++) {
      const [f, k] = i < 20 ? [b & c | ~b & d, 0x5a827999] : i < 40 ? [b ^ c ^ d, 0x6ed9eba1]
        : i < 60 ? [b & c | b & d | c & d, 0x8f1bbcdc] : [b ^ c ^ d, 0xca62c1d6];
      const t = (a << 5 | a >>> 27) + f + e + k + w[i] | 0;
      [e, d, c, b, a] = [d, c, b << 30 | b >>> 2, a, t];
    }
    [a, b, c, d, e].forEach((x, i) => {
      h[i] = h[i] + x | 0;
    });
  }
  return h.map((x) => (x >>> 0).toString(16).padStart(8, "0")).join("");
}
