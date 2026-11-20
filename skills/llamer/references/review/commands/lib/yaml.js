// Блочный YAML: ключи объектов по порядку, строки — в двойных кавычках по правилам JSON, которые YAML читает как есть.
"use strict";

const isObject = (v) => v !== null && typeof v === "object";

// inline — значение одной строкой; null — значение идёт блоком.
function inline(v) {
  if (Array.isArray(v)) {
    return v.length ? null : "[]";
  }
  if (isObject(v)) {
    return Object.keys(v).length ? null : "{}";
  }
  return v === null || v === undefined ? "null" : typeof v === "string" ? JSON.stringify(v) : String(v);
}

function block(value, pad) {
  if (Array.isArray(value)) {
    return value.flatMap((item) => {
      const one = inline(item);
      if (one !== null) {
        return [pad + "- " + one];
      }
      const [first, ...rest] = block(item, pad + "  ");
      return [pad + "- " + first.slice(pad.length + 2), ...rest];
    });
  }
  return Object.keys(value).flatMap((key) => {
    const one = inline(value[key]);
    return one !== null ? [pad + key + ": " + one] : [pad + key + ":", ...block(value[key], pad + "  ")];
  });
}

exports.yaml = (value) => block(value, "").join("\n") + "\n";
