const llamer = require("llamer");

exports.definition = {
  short: "Record the verdict: nature, risk and review depth",
  long: "Record the verdict on the change: does behaviour change, what a missed defect would cost, and how deep " +
    "the review has to go.",
  flags: [
    {name: "nature", type: "string", choices: ["mechanical", "behavioral"], usage: "does behaviour change"},
    {name: "risk", type: "string", choices: ["low", "medium", "high"], usage: "the same assessment in three steps"},
    {name: "level", type: "int", usage: "how deep the review goes: 1 (cheapest) to 5 (priciest)"},
    {name: "reason", type: "string", usage: "what justifies the verdicts"},
  ],
};

exports.command = (flags) => {
  if (!flags.nature || !flags.risk || !flags.reason) {
    throw new Error("нужны --nature, --risk, --level и --reason");
  }
  if (flags.level < 1 || flags.level > 5) {
    throw new Error("--level — от 1 до 5; пришло " + flags.level);
  }
  llamer.record({type: "assess", nature: flags.nature, risk: flags.risk, level: flags.level, reason: flags.reason});
  return "записано: assess";
};
