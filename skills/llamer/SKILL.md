---
name: llamer
description: Use when running agent workflows with llamer (`llamer run`), building a workflow for a project from a recipe such as code review, or reading a run's result.
---

# llamer

`llamer run <workflow>` runs a workflow script: a [cliche](https://github.com/hakastein/cliche) script that calls
agent stages — the `claude` or `codex` CLI in a strict sandbox — and gets their answers as values. Stdout is JSON
lines: stage events, and the result as the last line; the log goes to stderr.

## A project

- `.llamer.json` at the repository root holds the shared settings: `{"version": 2}`, and per driver `bin`, `env` and
  `readable` under `drivers.<name>`. A `codex` stage needs the directory of the codex installation in
  `drivers.codex.readable` (for npm — the `@openai/codex` package directory, `$(npm root -g)/@openai/codex`):
  without it every command of the agent fails in the sandbox.
- `.llamer/` at the repository root holds the workflows and everything they use; `llamer` looks for it upwards from
  the current directory (`--dir` or `LLAMER_DIR` name another one):

  ```text
  .llamer/
    workflows/<name>.js      workflow scripts: llamer run <name>
    commands/**/*.js         commands the agent calls
    lib/*.js                 libraries: .js files without exports.definition
    prompts/*.md             stage prompts
    schemas/*.json           answer schemas
    skills/<name>/SKILL.md   skills a stage may load
    runs/                    run artifacts, ignored by git
  ```

  Paths in a stage spec and in `llamer.prompt` are relative to `.llamer/`. Scripts take libraries with
  `require("../lib/x.js")` — never from outside `.llamer/`.

## A workflow script

```js
const llamer = require("llamer");

exports.definition = {
  short: "Write and check a joke",                // one line, 60 characters at most
  long: "Write a joke on a topic and check it.",
  flags: [{name: "topic", type: "string", usage: "what the joke is about"}],
};

const codex = {driver: "codex", model: "gpt-6.1-terra", effort: "low", timeout: "10m"};

exports.command = async (flags) => {
  const joke = await llamer.agent({...codex, name: "writer", schema: "schemas/joke.json",
    prompt: llamer.prompt("prompts/writer.md", {topic: flags.topic})}).result;
  const verdict = await llamer.agent({...codex, name: "critic", driver: "claude", model: "claude-sonnet-5-5",
    prompt: llamer.prompt("prompts/critic.md", {joke: JSON.stringify(joke)}), allow: ["notes post"],
    data: {topic: flags.topic}}).result;
  return {joke, verdict};                          // the result's `value`; return null when there is nothing
};
```

- `llamer.agent(spec)` starts one stage and returns `{started, result}` at once: `result` is a promise of its
  answer — the JSON value for a stage with `schema`, the text otherwise; `started` is a promise of the model's first
  response (or of the stage's end, if it never responded). A bad spec throws at the call, a failed stage rejects
  `result`; either fails the run even if the script catches it. A stage the script did not await is stopped when the
  script ends.
- Spec keys: `name` (`[a-z][a-z0-9_]*`, unique), `driver`, `model`, `effort`, `prompt` (the text), `timeout`
  (`10m`); optional `schema` — a JSON Schema the answer must match (codex wants every object strict:
  `additionalProperties: false`, every property in `required`); `skills` from `skills/`; `allow` — the commands the
  agent may run, as word prefixes without shell metacharacters: the project's commands (`"notes post"`) and any
  other (`"rg"`, `"git log"`); a call outside is refused, so name every command the prompt tells the agent to call;
  `readable` — paths the agent may read besides the context directory; `fetch` and `exec` — hosts and programs the
  stage's commands may use; `data` — a JSON value its commands read as
  `llamer.data`. Any other key is an error. Repeat shared keys with JS variables, not copies.
- `llamer.prompt(file, data)` — the file as a Go `text/template` on `data` (`{{.topic}}`); a missing key throws.
- `llamer.records()` — the run journal: what the commands of every stage recorded so far, `[{stage, value}]` in
  the order of writing.
- `llamer.env(name)` — an environment variable of `llamer`: tokens reach the workflow this way, never the agent.
- `llamer.fetch` and `llamer.exec` — as in commands, without the host and program lists: the workflow is the
  project's code.
- Stages run in parallel with `Promise.all`. Stages sharing the start of their prompts read it from the model's
  cache when the first one is started earlier and the rest once it responds:

  ```js
  const lead = llamer.agent({...codex, name: "logic", prompt: llamer.prompt("prompts/logic.md", data)});
  await lead.started;
  const [logic, tests] = await Promise.all([lead.result,
    llamer.agent({...codex, name: "tests", prompt: llamer.prompt("prompts/tests.md", data)}).result]);
  ```
- The agent reads only the context directory, its stage's skills and `readable`; it
  has no network.

## Commands

A command is a cliche script in `commands/`: `commands/notes/post.js` is `notes post`. It runs inside `llamer`,
outside the sandbox; the agent calls it by name from its shell.

```js
const llamer = require("llamer");

exports.definition = {
  short: "Post a note",
  long: "Post a note on the topic.",
  flags: [{name: "text", type: "string", usage: "the note"}],
};

exports.command = (flags) => {
  llamer.record({topic: llamer.data.topic, note: flags.text});
  return "recorded";                               // printed to the caller; an object is printed as JSON
};
```

- `llamer.data` — the stage's `data`; `llamer.record(value)` — writes to the run journal;
  `llamer.records()` — the run journal as the workflow sees it, the stage's own and parallel stages' records
  included.
- `llamer.fetch(url, {method, headers, body})` → `{status, body}` only to the stage's `fetch` hosts;
  `llamer.exec(program, [args], {stdin})` → `{stdout, stderr, exit}` only for its `exec` programs, in the caller's
  directory. Anything else fails the stage.
- A thrown error reaches the caller as stderr with exit code 1. Flag values are not required by cliche: check them
  in the script.

## Running

```bash
llamer run --dry-run review --base HEAD > /dev/null   # the script and every stage spec, without a model
llamer run review --base HEAD > run.jsonl
tail -n 1 run.jsonl | jq '{ok, failure, cost, value}'
```

- `llamer` flags go before the workflow name: `--dir <dir>`, `--context <dir>` — the agent's working directory,
  by default the current one, `--config <file>` — instead of `.llamer.json` at the repository root, `--dry-run`.
  Everything after the name belongs to the script: `llamer run <name> --help` lists its flags.
- `--dry-run` runs the script with a stub agent: an empty answer, or the smallest value of the stage's schema. Run
  it after every change of the workflow: a script error or a bad spec fails it before any paid call. The stub calls
  no commands, so it opens no sockets and runs inside an agent's sandbox too.
- The result: `ok`, `failure` (`stage`, `reason`, `detail`), `stages[]` with `answer`, `cost`, tokens, `value` —
  what the script returned, and `run` — the run directory `.llamer/runs/<date>_<time>/` next to the config:
  prompts, stage streams, `journal.jsonl` of command calls, `result.json`.
- Exit code `0` — the run passed, `1` — a stage or the script failed, or the run could not start (the reason is in
  stderr).

## Recipes

| Recipe | What it builds |
|---|---|
| [review](references/review/README.md) | A code review of local changes or of a GitLab merge request: a YAML report, threads in the merge request |

## The built-in review

`llamer` without a subcommand runs the built-in review: [references/builtin-review.md](references/builtin-review.md).
