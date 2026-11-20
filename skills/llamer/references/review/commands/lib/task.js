// Изменения, которые ревьюятся: от базы прогона до рабочего дерева.
"use strict";
const {Diff} = require("./diff.js");

// context — строк контекста в диффе задания и в `review diff`.
const context = 10;

// taskDiff — текст изменений и их разбор.
exports.taskDiff = (repo, base) => {
  const text = repo.diff(base, context);
  return {text, parsed: new Diff(text)};
};

const statuses = {A: "добавлен", M: "изменён", D: "удалён", R: "переименован"};

// files — строка на изменённый файл: путь, статус и число строк.
exports.files = (diff) => diff.files.map((f) => {
  const from = f.status === "R" ? " из " + f.oldPath : "";
  const [added, removed] = diff.count(f.path);
  return "- " + f.path + " — " + statuses[f.status] + from + ", " + (f.binary ? "бинарный" : "+" + added + " −" + removed);
});
