// Репозиторий ревью: git и файлы рабочего дерева. Команда исполняется в каталоге, где стоит агент,
// поэтому git зовётся от корня репозитория, а пути файлов — от него же, как в диффе.
"use strict";
const llamer = require("llamer");

// git — stdout git; код не из codes — ошибка с его stderr.
function git(args, codes = [0]) {
  const r = llamer.exec("git", args);
  if (!codes.includes(r.exit)) {
    throw new Error("git " + args.join(" ") + ": " + r.stderr.trim());
  }
  return r.stdout;
}

// Настройки пользователя не меняют вид диффа: префиксы, цвет, внешние диффы, переименования.
const diffFlags = ["--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "-M"];

// open — репозиторий, в котором стоит вызов.
exports.open = () => {
  const root = git(["rev-parse", "--show-toplevel"]).replace(/\n$/, "");
  const at = (args, codes) => git(["-C", root, ...args], codes);
  return {
    root,
    sha: (rev) => at(["rev-parse", "--verify", "--end-of-options", rev + "^{commit}"]).trim(),
    // diff — изменения от base до рабочего дерева с context строк контекста; неотслеживаемые файлы — добавленными.
    diff(base, context) {
      const flags = [...diffFlags, "-U" + context];
      const untracked = at(["ls-files", "--others", "--exclude-standard", "-z"]).split("\0").filter(Boolean);
      return at(["diff", ...flags, base, "--"]) +
        untracked.map((file) => at(["diff", ...flags, "--no-index", "--", "/dev/null", file], [1])).join("");
    },
    // lines — строки файла рабочего дерева; файла нет — null.
    lines(file) {
      const r = llamer.exec("cat", ["--", root + "/" + file]);
      return r.exit === 0 ? r.stdout.replace(/\n$/, "").split("\n") : null;
    },
  };
};
