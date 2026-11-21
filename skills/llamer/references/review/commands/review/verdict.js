const llamer = require("llamer");
const {current} = require("../../lib/state.js");

exports.definition = {
  short: "Pass a verdict on a recorded finding: stands or refuted",
  long: "Pass a verdict on a recorded finding: stands or refuted. A revised severity goes in the same call.",
  flags: [
    {name: "id", type: "int", usage: "the #N of the finding from the list in your task"},
    {name: "refuted", type: "string", choices: ["true", "false"], usage: "does the finding fall"},
    {name: "reason", type: "string", usage: "the decisive fact with file and line: what refutes it, or why it " +
      "stands and which step of the scale you confirmed"},
    {name: "severity", type: "string", choices: ["minor", "major", "critical"], usage: "the revised severity; goes only together with --severity-reason"},
    {name: "severity-reason", type: "string", usage: "what the revised severity rests on"},
  ],
};

exports.command = (flags) => {
  if (!flags.refuted || !flags.reason) {
    throw new Error("нужны --id, --refuted и --reason");
  }
  const f = current(llamer.data).byId(flags.id);
  if (!f) {
    throw new Error(`находки с номером ${flags.id} нет; список — review list`);
  }
  if (f.duplicateOf) {
    throw new Error(`находка #${flags.id} помечена повтором #${f.duplicateOf} и до автора не дойдёт: вердикт ей ` +
      "не нужен. Суди находки из списка в своём задании");
  }
  if (flags.severity && !flags["severity-reason"]) {
    throw new Error("вместе с --severity нужен --severity-reason");
  }
  llamer.record({type: "verdict", id: flags.id, refuted: flags.refuted === "true", reason: flags.reason,
    severity: flags.severity || null, severityReason: flags["severity-reason"] || null});
  return `записано: verdict #${flags.id} ${flags.refuted === "true" ? "опровергнута" : "в силе"}`;
};
