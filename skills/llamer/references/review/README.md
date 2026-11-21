# Recipe: code review

A workflow that reviews a change — everything from a base revision to the working tree, untracked files included —
and returns a YAML report; with a merge request it also reads its threads and publishes the review to it.
`workflows/review.js` calls the stages:

| Stage | What it does |
|---|---|
| `assess` | nature, risk and depth of the change: `review assess` |
| `threads` | merge request only: closes our open threads the current code settled, `review resolve` |
| `logic`, `quality`, `tests`, `comments` | findings (`review finding`); `logic` closes with `review summary` |
| `dedupe` | marks duplicates: `review dupe` |
| `skeptic` | a verdict on every finding: `review verdict` |

The stages run in waves: `assess` with `threads`, then `logic`, `quality`, `tests` and `comments` together, then
`dedupe`, then `skeptic`. The first stage of a wave starts alone, the rest once it responds, so they read the shared
start of the prompt from the model's cache; the next wave starts when the whole wave has ended:

```js
const wave = async (lead, ...rest) => {
  const first = start(lead);
  await first.started;
  await Promise.all([first.result, ...rest.map((stage) => start(stage).result)]);
};
```

The materials — the diff, the file list, the merge request threads and the task — are built by the workflow
(`lib/materials.js`) and start every prompt (`{{.materials}}`): the stages share it as the cached prompt prefix.
The state of the review — findings, duplicates, verdicts — is a fold of records (`lib/state.js`): the workflow's own
(the base revision, the threads), which every stage gets in `data`, and the run journal (`llamer.records()`). The
`review` commands read the journal at each call, so they see what any stage has recorded so far, their own and
parallel stages' records included; a finding's number is its place in the order of writing. The report, the
findings for the `dedupe` and `skeptic` prompts and the publication are library calls of the workflow
(`lib/report.js`, `lib/findings.js`, `lib/publish.js`).

## Build it in a project

1. Copy this directory without `README.md` into the project as `.llamer/` and commit it alone: the review takes
   uncommitted and untracked files as part of the change, and the change itself must stay out of that commit —
   `git add .llamer .llamer.json && git commit -m "Review workflow" -- .llamer .llamer.json`. A `.llamer/.gitignore`
   left by the built-in review hides the files from git: remove it first.
2. Make sure `.llamer.json` exists at the repository root: `{"version": 2}` is enough for `claude`. A `codex` stage
   also needs `drivers.codex.readable` — see the `llamer` skill.
3. Pick `driver`, `model` and `effort` per stage in `workflows/review.js`. A stage may run on another driver: the
   prompts and commands do not depend on it. The same stage on two drivers is two calls with their own names
   (`logic`, `logic_codex`) and the same prompt file.
4. Project knowledge goes into the prompts and into `skills/`: a skill named in a stage's `skills` is offered to it.
   Project stages (`docs`, `migrations`) are copies of `prompts/quality.md` with their own task, added to the wave of
   `logic` with the same `allow`.
5. Check it without a model: `llamer run --dry-run review --base HEAD`. It must end with `"ok":true`.

## Run it

| What to review | `--base` |
|---|---|
| uncommitted changes, untracked files included | `HEAD` |
| the branch against its target, with uncommitted | `$(git merge-base origin/main HEAD)` |
| a commit, on a clean tree checked out at it | `HEAD^` |

```bash
llamer run review --base HEAD --task "$(cat task.md)" > run.jsonl
tail -n 1 run.jsonl | jq -r .value.report
```

`--task` is the text of the task the change implements; leave it out when there is none. `llamer run review --help`
lists the flags.

## A merge request

In CI the tree is the merge request head:

```bash
GITLAB_TOKEN=… llamer run review --base "$CI_MERGE_REQUEST_DIFF_BASE_SHA" \
  --mr "$CI_MERGE_REQUEST_PROJECT_URL/-/merge_requests/$CI_MERGE_REQUEST_IID" --publish
```

- With `--mr` the workflow reads the merge request threads first: the materials carry what is already said in it,
  and the `threads` stage gets our open threads to check against the current code.
- `GITLAB_TOKEN` — a token with the `api` scope. It reaches only the workflow, never the agent, and is redacted from
  the run's output and journal.
- Without `--publish` nothing is written to the merge request: `value.publication` lists the write requests.
  With it — what was published.

## Commands

A stage calls only the `review` commands in its `allow`; the agent sees their flags in `review <command> --help`.
Every command checks its input and refuses with the reason. `allow` may also name other programs the agent may run
(`"rg"`, `"git log"`).

| Command | Records | What it does |
|---|---|---|
| `review assess --nature --risk --level --reason` | `assess` | the verdict on the change |
| `review finding --file --line --line-end --text --severity --severity-reason --related [--suggestion] [--deleted] [--trace]` | `finding` | a remark anchored to changed lines: both ends must be added lines, or removed ones with `--deleted`; a suggestion must change the fragment |
| `review summary --text` | `summary` | the stage's conclusion; a second call replaces it |
| `review stop --reason` | `stop` | the stage gave up: a gap in the report |
| `review list` | — | every finding recorded so far, by any stage, with its number and state |
| `review diff [path...]` | — | the numbered diff of files, or the file list |
| `review dupe --id --of --reason` | `dupe` | a duplicate drops out |
| `review verdict --id --refuted --reason [--severity --severity-reason]` | `verdict` | a refuted finding drops out |
| `review resolve --id --reason` | `resolve` | closes an open thread of ours that the current code settled |

The commands need `git` and `cat` (`exec` of every stage).

## The report

```yaml
base: "<sha>"
assessment: {nature, risk, level, reason}   # null without an assess verdict
findings:                                    # not duplicates, not refuted, no verbatim repeats
  - id: 1
    stage: "logic"
    file: "src/Order.php"
    line: 12
    lineEnd: 14
    deleted: false                           # true — lines of the old side
    severity: "major"                        # minor, major, critical
    severityReason: "…"
    text: "…"
    suggestion: null                         # replaces lines line..lineEnd
    related: [{file, line, lineEnd}]
    trace: null
summaries: [{stage, text}]
resolvedThreads: [{id, reason, place}]
gaps: ["…"]                                  # what the review did not cover: a stopped stage, findings without a verdict
```
