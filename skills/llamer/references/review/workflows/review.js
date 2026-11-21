// Ревью изменений: от базовой ревизии до рабочего дерева, неотслеживаемые файлы тоже. Итог — отчёт YAML; с --mr —
// ещё и треды в merge request GitLab.
const llamer = require("llamer");
const repo = require("../lib/repo.js");
const gitlab = require("../lib/gitlab.js");
const threads = require("../lib/threads.js");
const {state, current} = require("../lib/state.js");
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

exports.command = async (flags) => {
  if (!flags.base) {
    throw new Error("нужен --base");
  }
  if (flags.publish && !flags.mr) {
    throw new Error("--publish — только вместе с --mr");
  }
  const r = repo.open();
  // Свои записи ревью: база и треды. Этап получает их в llamer.data, записанное этапами — в журнале прогона.
  const own = [{stage: "materials", value: {type: "materials", base: r.sha(flags.base)}}];
  let mr = null;
  if (flags.mr) {
    mr = gitlab.open(flags.mr, llamer.env("GITLAB_TOKEN"), !flags.publish);
    own.push({stage: "threads", value: {type: "threads", threads: threads.summarise(mr.discussions())}});
  }
  const shared = materials(r, state(own).materials.base, state(own).threads, flags.task ?? "");
  // Этап — спек llamer.agent и vars: данные его задания сверх общих материалов.
  const start = ({vars = {}, ...spec}) =>
    llamer.agent({...spec, data: own, prompt: llamer.prompt(`prompts/${spec.name}.md`, {materials: shared, ...vars})});
  // Волна — этапы, которые идут параллельно: первый ведёт, остальные стартуют с началом его ответа и читают общее
  // начало задания из кэша.
  const wave = async (lead, ...rest) => {
    const first = start(lead);
    await first.started;
    await Promise.all([first.result, ...rest.map((stage) => start(stage).result)]);
  };

  const assess = {...claude, name: "assess", model: "claude-sonnet-5-5", effort: "medium", timeout: "10m",
    skills: ["assessing-review-scope"], allow: ["review assess", "review diff", "review stop"]};
  const replies = {...claude, name: "threads", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m",
    allow: ["review resolve", "review list", "review diff", "review stop"]};
  await wave(assess, ...(mr ? [{...replies, vars: {open_threads: threads.openList(state(own).threads)}}] : []));

  const finder = ["review finding", "review list", "review diff", "review stop"];
  await wave(
    {...claude, name: "logic", model: "claude-opus-5-5", effort: "low", timeout: "30m",
      skills: ["reviewing-business-logic"], allow: ["review finding", "review list", "review summary", "review diff", "review stop"]},
    {...claude, name: "quality", model: "claude-sonnet-5-5", effort: "high", timeout: "30m",
      skills: ["reviewing-code-quality"], allow: finder},
    {...claude, name: "tests", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m", allow: finder},
    {...claude, name: "comments", model: "claude-sonnet-5-5", effort: "medium", timeout: "20m", allow: finder});

  await wave({...claude, name: "dedupe", model: "claude-sonnet-5-5", effort: "medium", timeout: "10m",
    allow: ["review dupe", "review list", "review diff", "review stop"], vars: {found: listing(current(own), r, false)}});
  await wave({...claude, name: "skeptic", model: "claude-opus-5-5", effort: "low", timeout: "30m",
    skills: ["refuting-review-findings"], allow: ["review verdict", "review list", "review diff", "review stop"],
    vars: {deduped: listing(current(own), r, true)}});

  const s = current(own);
  if (!mr) {
    return {report: report(s)};
  }
  const published = publish(mr, s, r);
  return {report: report(s), publication: flags.publish ? published : mr.planned};
};
