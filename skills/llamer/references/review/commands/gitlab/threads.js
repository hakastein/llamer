const llamer = require("llamer");
const gitlab = require("../lib/gitlab.js");
const threads = require("../lib/threads.js");

exports.definition = {
  short: "Load the threads of a merge request",
  long: "Load the line threads of a GitLab merge request and record them: `review materials` adds them to the " +
    "shared prompt part, `review resolve` closes ours. Prints our open threads for the threads stage's prompt. " +
    "Meant for a command stage before `review materials`.",
  flags: [
    {name: "mr", type: "string", usage: "the merge request URL"},
    {name: "token", type: "string", usage: "a GitLab token with the api scope"},
  ],
};

exports.command = (flags) => {
  const all = threads.summarise(gitlab.open(flags.mr, flags.token, false).discussions());
  llamer.record({type: "threads", threads: all});
  return threads.open(all).map((t) => `- id ${t.id} — ${t.file}:${t.line}\n  ${t.text}` +
    (t.reply ? "\n  author's reply: " + t.reply : "")).join("\n") || "- there are no open threads of ours";
};
