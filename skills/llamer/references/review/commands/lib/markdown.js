// Разметка заметок GitLab: экранирование формул и метка ревизии прогона.
"use strict";

const fence = /^ {0,3}(`{3,}|~{3,})(.*)$/;

// codeRanges — участки кода в тексте Markdown, [начало, конец): строки огороженных блоков и блоков с отступом,
// содержимое спанов в остальном тексте — по правилам CommonMark.
// shortcut: блоки внутри списков и цитат не распознаются — их `$` экранируется; разбирать, если такие тексты пойдут.
function codeRanges(text) {
  const ranges = [];
  let open = null; // открывающий забор блока
  let paragraph = null; // начало абзаца, в котором ищутся спаны
  let offset = 0;
  const flush = (end) => {
    if (paragraph !== null) {
      ranges.push(...spans(text, paragraph, end));
      paragraph = null;
    }
  };
  for (const line of text.split("\n")) {
    const start = offset;
    const end = start + line.length;
    offset = end + 1;
    const m = fence.exec(line);
    if (open) {
      if (m && m[1][0] === open[0] && m[1].length >= open.length && m[2].trim() === "") {
        open = null;
      } else {
        ranges.push([start, end]);
      }
    } else if (m && !(m[1][0] === "`" && m[2].includes("`"))) {
      flush(start);
      open = m[1];
    } else if (line.trim() === "") {
      flush(start);
    } else if (paragraph === null && /^( {4}|\t)/.test(line)) {
      ranges.push([start, end]);
    } else if (paragraph === null) {
      paragraph = start;
    }
  }
  flush(text.length);
  return ranges;
}

// spans — содержимое спанов кода в тексте [from, to): строка из n обратных кавычек закрывается ровно такой же.
function spans(text, from, to) {
  const out = [];
  let i = from;
  while (i < to) {
    if (text[i] === "\\") {
      i += 2;
      continue;
    }
    if (text[i] !== "`") {
      i++;
      continue;
    }
    let n = 0;
    while (text[i + n] === "`") {
      n++;
    }
    let j = i + n;
    let close = -1;
    while (j < to) {
      if (text[j] !== "`") {
        j++;
        continue;
      }
      let k = 0;
      while (text[j + k] === "`") {
        k++;
      }
      if (k === n) {
        close = j;
        break;
      }
      j += k;
    }
    if (close < 0) {
      i += n;
      continue;
    }
    out.push([i + n, close]);
    i = close + n;
  }
  return out;
}

// escapeDollarsOutsideCode — GitLab рендерит `$…$` как формулу KaTeX, и текст с переменными превращается в кашу.
// Внутри кода `$` не рендерится, а `\$` показался бы буквально.
exports.escapeDollarsOutsideCode = (text) => {
  let out = "";
  let last = 0;
  for (const [start, end] of codeRanges(text)) {
    out += text.slice(last, start).replaceAll("$", "\\$") + text.slice(start, end);
    last = end;
  }
  return out + text.slice(last).replaceAll("$", "\\$");
};

const revisionMark = "llamer-rev";

// withRevisionMark — метка ревизии прогона: по ней следующий прогон узнаёт свои треды и свою точку отсчёта.
exports.withRevisionMark = (body, headSha) => headSha ? body + "\n\n<sub>ревизия <code>" + headSha.slice(0, 8) +
  "</code></sub><!-- " + revisionMark + ":" + headSha + " -->" : body;

// hasRevisionMark — текст несёт метку ревизии: это заметка прогона.
exports.hasRevisionMark = (body) => /<!--\s*llamer-rev:[0-9a-f]{7,40}\s*-->/.test(body);

// StatusMarker — начало заметки-статуса прогона.
exports.statusMarker = "<!-- llamer-status -->";
