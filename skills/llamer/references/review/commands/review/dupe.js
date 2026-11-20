const llamer = require("llamer");
const {state} = require("../lib/state.js");

exports.definition = {
  short: "Mark a finding as a duplicate of another one",
  long: "Mark a finding as a duplicate of another one: same defect, same place, same cause. The marked one drops " +
    "out — --of should be the better-written of the two.",
  flags: [
    {name: "id", type: "int", usage: "the number of the duplicate finding"},
    {name: "of", type: "int", usage: "the number of the finding that stays"},
    {name: "reason", type: "string", usage: "why this is the same place and the same cause"},
  ],
};

exports.command = (flags) => {
  if (!flags.reason) {
    throw new Error("нужны --id, --of и --reason");
  }
  const s = state();
  const find = (flag, n) => {
    const f = s.byId(n);
    if (!f) {
      throw new Error(`${flag}: находки с номером ${n} нет; список — review list`);
    }
    return f;
  };
  const duplicate = find("--id", flags.id);
  const original = find("--of", flags.of);
  if (flags.id === flags.of) {
    throw new Error("--id и --of совпадают: находка не может повторять саму себя");
  }
  if (duplicate.duplicateOf) {
    throw new Error(`находка #${flags.id} уже помечена повтором #${duplicate.duplicateOf}`);
  }
  if (original.duplicateOf) {
    throw new Error(`#${flags.of} сама помечена повтором #${original.duplicateOf}: в --of ставится оставляемая ` +
      `находка, а не другой повтор — вероятно, это #${original.duplicateOf}`);
  }
  llamer.record({type: "dupe", id: flags.id, of: flags.of, reason: flags.reason});
  return `записано: dupe #${flags.id} повтор #${flags.of}`;
};
