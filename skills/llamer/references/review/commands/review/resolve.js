const llamer = require("llamer");
const {state} = require("../lib/state.js");

exports.definition = {
  short: "Close an open review thread the current code has settled",
  long: "Close a thread of ours that earlier runs left open. Treat the remark as a REQUIREMENT: it is settled once " +
    "the current code satisfies it, wherever that code now lives.",
  flags: [
    {name: "id", type: "string", usage: "the thread id from the list in your task"},
    {name: "reason", type: "string", usage: "what exactly settled the remark in this revision"},
  ],
};

exports.command = (flags) => {
  if (!flags.id || !flags.reason) {
    throw new Error("нужны --id и --reason");
  }
  const open = state().threads.filter((t) => t.ours && !t.resolved);
  const t = open.find((x) => x.id === flags.id);
  if (!t) {
    throw new Error(`треда «${flags.id}» среди открытых тредов прогона нет. Открытые:\n` +
      open.map((x) => x.id + " — " + x.file + ":" + x.line).join("\n"));
  }
  llamer.record({type: "resolve", id: t.id, place: t.file + ":" + t.line, reason: flags.reason});
  return "записано: resolve " + t.file + ":" + t.line;
};
