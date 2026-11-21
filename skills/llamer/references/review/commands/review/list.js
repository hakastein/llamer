const llamer = require("llamer");
const {state, at} = require("../../lib/state.js");

exports.definition = {
  short: "Print every finding: number, severity and place",
  long: "Print number, severity and place of every finding the earlier stages recorded, including findings " +
    "already dropped from publication ([refuted], [duplicate of #N]). A write confirms without a number, so this " +
    "is where a number comes from.",
};

const oneLine = (text, n) => [...text.split(/\s+/).join(" ")].slice(0, n).join("");

exports.command = () => {
  const lines = state(llamer.data).findings.map((f) => {
    const mark = f.duplicateOf ? "повтор #" + f.duplicateOf : f.refuted ? "опровергнута" : f.severity;
    return `${f.id}. [${mark}] ${at(f)} — ${oneLine(f.text, 120)}`;
  });
  return lines.join("\n") || "находок пока нет";
};
