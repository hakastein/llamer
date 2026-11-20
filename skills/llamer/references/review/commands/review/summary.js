const llamer = require("llamer");

exports.definition = {
  short: "Close the review with the verdict on the whole change",
  long: "Close the review with the verdict on the whole change. A second call replaces the first.",
  flags: [{name: "text", type: "string", usage: "the verdict on the change as a whole"}],
};

exports.command = (flags) => {
  if (!flags.text) {
    throw new Error("нужен --text");
  }
  llamer.record({type: "summary", text: flags.text});
  return "записано: summary";
};
