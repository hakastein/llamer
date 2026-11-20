const repo = require("../lib/repo.js");
const {number, chunks} = require("../lib/diff.js");
const {files, taskDiff} = require("../lib/task.js");
const {state} = require("../lib/state.js");

exports.definition = {
  short: "Print the numbered diff of the given files",
  long: "The change's diff of the given files, numbered and widened as in the materials. Several paths per call. " +
    "Without paths: every changed file with its status and line counts.",
  args: [{name: "path", type: "path", usage: "a repo-root-relative path, as in the diff", multiple: true, optional: true}],
};

exports.command = (paths) => {
  const diff = taskDiff(repo.open(), state().materials.base);
  if (!paths?.length) {
    return files(diff.parsed).join("\n") || "изменений нет";
  }
  const byFile = chunks(diff.text);
  return paths.map((path) => byFile.has(path) ? number(byFile.get(path)).trimEnd()
    : path + ": среди изменений такого файла нет; перечень — review diff без аргументов").join("\n");
};
