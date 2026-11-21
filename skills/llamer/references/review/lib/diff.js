// Дифф: строки обеих сторон по файлам и нумерация строк для задания этапа.
"use strict";

const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;
const gitHeader = /^diff --git a\/(.*) b\/(.*)$/;

// Diff — разобранный unified diff. added — новые строки файла с номером старой стороны на их месте,
// removed — наоборот: GitLab адресует строку парой «старый номер, новый номер», и без неё многострочный тред
// создаётся, а подсветка диапазона не встаёт.
class Diff {
  constructor(text) {
    this.files = []; // {path, oldPath, newPath, status: A|D|M|R, binary}
    this.added = new Map();
    this.removed = new Map();
    let file = null;
    let oldPos = 0;
    let newPos = 0;
    let inHunk = false;
    const side = (map, path) => {
      if (!map.has(path)) {
        map.set(path, {order: [], byNum: new Map()});
      }
      return map.get(path);
    };
    for (const row of text.split("\n")) {
      const head = gitHeader.exec(row);
      if (head) {
        file = {oldPath: head[1], newPath: head[2], status: "M", binary: false};
        this.files.push(file);
        inHunk = false;
        continue;
      }
      if (!file) {
        continue;
      }
      const m = hunk.exec(row);
      if (m) {
        [oldPos, newPos, inHunk] = [Number(m[1]), Number(m[2]), true];
        continue;
      }
      if (!inHunk) {
        if (row.startsWith("new file mode")) {
          file.status = "A";
        } else if (row.startsWith("deleted file mode")) {
          file.status = "D";
        } else if (row.startsWith("rename from ")) {
          [file.oldPath, file.status] = [row.slice(12), "R"];
        } else if (row.startsWith("rename to ")) {
          file.newPath = row.slice(10);
        } else if (row.startsWith("Binary files ")) {
          file.binary = true;
        } else if (row.startsWith("--- ")) {
          file.oldPath = row === "--- /dev/null" ? null : row.slice(6);
        } else if (row.startsWith("+++ ")) {
          file.newPath = row === "+++ /dev/null" ? null : row.slice(6);
        }
        continue;
      }
      if (row.startsWith("-")) {
        set(side(this.removed, file.oldPath), oldPos++, {other: newPos, text: row.slice(1)});
      } else if (row.startsWith("+")) {
        set(side(this.added, file.newPath), newPos++, {other: oldPos, text: row.slice(1)});
      } else if (row.startsWith(" ")) {
        oldPos++;
        newPos++;
      }
    }
    for (const f of this.files) {
      f.path = f.newPath ?? f.oldPath;
      if (f.status === "A") {
        f.oldPath = null;
      }
      if (f.status === "D") {
        f.newPath = null;
      }
    }
  }

  side(deleted) {
    return deleted ? this.removed : this.added;
  }

  touches(file, deleted) {
    return this.side(deleted).has(file);
  }

  linesOf(file, deleted) {
    return this.side(deleted).get(file)?.order ?? [];
  }

  line(file, number, deleted) {
    return this.side(deleted).get(file)?.byNum.get(number);
  }

  hasLine(file, number, deleted) {
    return this.line(file, number, deleted) !== undefined;
  }

  // pairAt — [старый номер, новый номер] строки среди изменений.
  pairAt(file, number, deleted) {
    const other = this.line(file, number, deleted).other;
    return deleted ? [number, other] : [other, number];
  }

  // oldPathOf — путь до переименования: GitLab не принимает позицию, где у переименованного файла old_path
  // равен new_path, и вешает тред на реквест целиком.
  oldPathOf(file) {
    return this.files.find((f) => f.path === file)?.oldPath ?? file;
  }

  // count — добавленные и удалённые строки файла.
  count(file) {
    const f = this.files.find((x) => x.path === file);
    return [this.linesOf(f.newPath, false).length, this.linesOf(f.oldPath, true).length];
  }
}

function set(lines, number, line) {
  if (!lines.byNum.has(number)) {
    lines.order.push(number);
  }
  lines.byNum.set(number, line);
}

exports.Diff = Diff;

const width = 5;

// number — дифф с номером строки текущего дерева у каждой строки ханка: готовый `path:line` для `review`.
// У удалённой строки в дереве нет места, номер ей не ставится.
exports.number = (text) => {
  let line = -1;
  return text.split("\n").map((row) => {
    if (row.startsWith("diff --git ")) {
      line = -1;
      return row;
    }
    const m = hunk.exec(row);
    if (m) {
      line = Number(m[2]);
      return row;
    }
    if (line < 0 || row.startsWith("\\")) {
      return row;
    }
    if (row.startsWith("-")) {
      return " ".repeat(width) + " " + row;
    }
    if (row.startsWith("+") || row.startsWith(" ")) {
      return String(line++).padStart(width) + " " + row;
    }
    return row;
  }).join("\n");
};

// chunks — дифф по файлам: путь → текст его части.
exports.chunks = (text) => {
  const out = new Map();
  const starts = [...text.matchAll(/^diff --git .*$/gm)];
  starts.forEach((m, i) => {
    const chunk = text.slice(m.index, i + 1 < starts.length ? starts[i + 1].index : text.length);
    out.set(new Diff(chunk).files[0].path, chunk);
  });
  return out;
};
