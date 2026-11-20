---
name: llamer
description: Use when running agent workflows with llamer (`llamer run`), building a workflow for a project from a recipe such as code review, or reading a run's result.
---

# llamer

`llamer run <workflow dir>` runs a workflow: agent stages — the `claude` or `codex` CLI in a strict sandbox — and
command stages without an agent, in order and in waves. A stage's answer goes into the prompts of the later stages.
Stdout is JSON lines: stage events, and the result as the last line; the log goes to stderr.

## A project

- `.llamer.json` at the repository root holds the shared settings: `{"version": 2}`, and per driver `bin`, `env` and
  `readable` under `drivers.<name>`. A `codex` stage needs the directory of the codex installation in
  `drivers.codex.readable` (for npm — the `@openai/codex` package directory, `$(npm root -g)/@openai/codex`):
  without it every command of the agent fails in the sandbox.
- A workflow is a directory of its own:

  ```text
  workflow.json         stages and their order
  prompts/*.md          stage prompt templates
  skills/<name>/SKILL.md skills a stage may load
  commands/**/*.js      commands the agent calls, and command stages run
  ```

## workflow.json

```json
{ "version": 1,
  "readable": ["/srv/glossary"],
  "commands": { "hosts": ["gitlab.example.com"], "programs": ["git"] },
  "stages": [
    { "name": "facts", "command": ["notes", "facts", "--issue", "${ISSUE}"] },
    [ { "name": "writer", "driver": "codex", "model": "gpt-6.1-terra", "effort": "low", "prompt": "prompts/writer.md",
        "skills": ["style"], "schema": "answer.schema.json", "timeout": "10m" },
      { "name": "critic", "driver": "claude", "model": "claude-sonnet-5-5", "effort": "low",
        "prompt": "prompts/critic.md", "timeout": "10m" } ],
    { "name": "post", "command": ["notes", "post"] } ] }
```

- An agent stage: `name` (`[a-z][a-z0-9_]*`), `driver`, `model`, `effort`, `prompt`, `timeout`; optional `skills`
  from `skills/` and `schema` — a JSON Schema the answer must match (codex wants every object strict:
  `additionalProperties: false`, every property in `required`).
- A command stage: `name` and `command` — the argv of a command from `commands/`. `${VAR}` in it comes from the
  environment of `llamer`: this is how run inputs and tokens reach a workflow — the agent never sees them. Its stdout
  is the stage's answer; a non-zero exit fails the run.
- An array of stages is a wave: they run at once. The first one leads, the rest start when the model has begun to
  answer it — so put the stages sharing the longest prompt prefix in one wave, and start every prompt with that
  prefix.
- A prompt is a Go `text/template`: `{{.Answers.<stage>}}` is the answer of a stage of an earlier wave — JSON for a
  stage with a schema, text otherwise.
- The agent reads only the context directory, its stage's skills and `readable`; it has no network. `readable` paths
  are absolute or relative to the workflow directory.

## Commands

A command is a [cliche](https://github.com/hakastein/cliche) script: `commands/notes/post.js` is `notes post`. It runs
inside `llamer`, outside the sandbox; the agent calls it by name from its shell. A `.js` file without
`exports.definition` is a library the scripts `require` by a relative path.

```js
const llamer = require("llamer");

exports.definition = {
  short: "Record a note",                     // one line, 60 characters at most
  long: "Record a note in the run journal.",
  flags: [{ name: "text", type: "string", usage: "the note" }],
};

exports.command = (flags) => {
  llamer.record({ note: flags.text });
  return "recorded";                          // printed to the caller; an object is printed as JSON
};
```

- `llamer.record(value)` writes to the run journal; `llamer.results()` reads back everything recorded so far as
  `[{stage, value}]` — this is how stages share state.
- `llamer.fetch(url, {method, headers, body})` → `{status, body}` only to `commands.hosts`;
  `llamer.exec(program, [args], {stdin})` → `{stdout, stderr, exit}` only for `commands.programs`, in the caller's
  directory. Anything else fails the stage.
- A thrown error reaches the caller as stderr with exit code 1. Flag values are not required by cliche: check them
  in the script.

## Running

```bash
REVIEW_BASE=HEAD llamer run workflows/review --context . > run.jsonl
tail -n 1 run.jsonl | jq '{ok, failure, cost}'
```

- `--context <dir>` — the agent's working directory, by default the current one; `--config <file>` — instead of
  `.llamer.json` at the repository root.
- The result: `ok`, `failure` (`stage`, `reason`, `detail`), `stages[]` with `answer`, `cost`, tokens, and `run` —
  the run directory `.llamer/<date>_<time>/` next to the config: prompts, stage streams, `journal.jsonl` of command
  calls, `result.json`.
- Exit code `0` — every stage passed, `1` — a stage failed or the run could not start (the reason is in stderr).

## Recipes

| Recipe | What it builds |
|---|---|
| [review](references/review/README.md) | A code review of local changes or of a GitLab merge request: a YAML report, threads in the merge request |

## The built-in review

`llamer` without a subcommand runs the built-in review: [references/builtin-review.md](references/builtin-review.md).
