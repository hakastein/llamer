// Ревью изменений: от базовой ревизии до рабочего дерева, неотслеживаемые файлы тоже. Итог — отчёт YAML; с --mr —
// ещё и треды в merge request GitLab.
const llamer = require("llamer");
const repo = require("../lib/repo.js");
const gitlab = require("../lib/gitlab.js");
const threads = require("../lib/threads.js");
const {state} = require("../lib/state.js");
const {materials} = require("../lib/materials.js");
const {listing} = require("../lib/findings.js");
const {report} = require("../lib/report.js");
const {publish} = require("../lib/publish.js");

exports.definition = {
  short: "Review a change",
  long: "Review the change from the base revision to the working tree, untracked files included, and print the " +
    "report as YAML. With --mr also read the threads of the merge request and publish the review to it; the token " +
    "comes from GITLAB_TOKEN.",
  flags: [
    {name: "base", type: "string", usage: "the revision the change is counted from"},
    {name: "task", type: "string", usage: "the text of the task the change implements"},
    {name: "mr", type: "string", usage: "the merge request URL, https://gitlab.example.com/group/project/-/merge_requests/1"},
    {name: "publish", type: "bool", usage: "with --mr: send the threads; without it the write requests are only listed"},
  ],
};

// Программы команд этапов: дифф и файлы рабочего дерева.
const tools = {exec: ["git", "cat"]};
const claude = {driver: "claude", ...tools};

exports.command = (flags) => {
  if (!flags.base) {
    throw new Error("нужен --base");
  }
  if (flags.publish && !flags.mr) {
    throw new Error("--publish — только вместе с --mr");
  }
  const r = repo.open();
  // Записи ревью: свои и записанное командами этапов, по порядку. Этап получает их в llamer.data.
  const log = [{stage: "materials", value: {type: "materials", base: r.sha(flags.base)}}];
  let mr = null;
  if (flags.mr) {
    mr = gitlab.open(flags.mr, llamer.env("GITLAB_TOKEN"), !flags.publish);
    log.push({stage: "threads", value: {type: "threads", threads: threads.summarise(mr.discussions())}});
  }
  const shared = materials(r, state(log).materials.base, state(log).threads, flags.task ?? "");
  const run = (spec, data = {}) => {
    llamer.agent({...spec, data: log, prompt: llamer.prompt(`prompts/${spec.name}.md`, {materials: shared, ...data})});
    log.push(...llamer.records(spec.name).map((value) => ({stage: spec.name, value})));
  };

  // shortcut: последовательно до промисов cliche (DEV-15367); волна — assess и threads, затем logic, quality,
  // tests и comments: первый этап волны ведёт, остальные стартуют с началом его ответа и читают общее начало
  // задания из кэша.
  run({...claude, name: "assess", model: "claude-sonnet-5-5", effort: "medium", timeout: "10m",
    skills: ["assessing-review-scope"], allow: ["review assess", "review diff", "review stop"]});
  if (mr) {
    run({...claude, name: "threads", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m",
      allow: ["review resolve", "review list", "review diff", "review stop"]},
    {open_threads: threads.openList(state(log).threads)});
  }
  const finder = ["review finding", "review list", "review diff", "review stop"];
  run({...claude, name: "logic", model: "claude-opus-5-5", effort: "low", timeout: "30m",
    skills: ["reviewing-business-logic"], allow: ["review finding", "review list", "review summary", "review diff", "review stop"]});
  run({...claude, name: "quality", model: "claude-sonnet-5-5", effort: "high", timeout: "30m",
    skills: ["reviewing-code-quality"], allow: finder});
  run({...claude, name: "tests", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m", allow: finder});
  run({...claude, name: "comments", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m", allow: finder});

  run({...claude, name: "dedupe", model: "claude-sonnet-5-5", effort: "medium", timeout: "10m",
    allow: ["review dupe", "review list", "review diff", "review stop"]}, {found: listing(state(log), r, false)});
  run({...claude, name: "skeptic", model: "claude-opus-5-5", effort: "low", timeout: "30m",
    skills: ["refuting-review-findings"], allow: ["review verdict", "review list", "review diff", "review stop"]},
  {deduped: listing(state(log), r, true)});

  const s = state(log);
  if (!mr) {
    return {report: report(s)};
  }
  const published = publish(mr, s, r);
  return {report: report(s), publication: flags.publish ? published : mr.planned};
};
