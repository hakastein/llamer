const llamer = require("llamer");

exports.definition = {
  short: "Abort the stage and say why",
  long: "Abort the stage and say why: the report lists the stage as a gap of the review.",
  flags: [{name: "reason", type: "string", usage: "why the review was aborted"}],
};

exports.command = (flags) => {
  if (!flags.reason) {
    throw new Error("нужен --reason");
  }
  llamer.record({type: "stop", reason: flags.reason});
  return "записано: stop";
};
