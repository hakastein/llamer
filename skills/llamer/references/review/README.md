# Recipe: code review

A workflow that reviews a change — everything from a base revision to the working tree, untracked files included —
and prints a YAML report. Stages:

| Stage | Kind | What it does |
|---|---|---|
| `materials` | command | `review materials`: the diff, the file list and the task, as the shared start of every prompt |
| `assess` | agent | nature, risk and depth of the change: `review assess` |
| `logic`, `quality`, `tests`, `comments` | agents, one wave | findings (`review finding`) and a summary (`review summary`) |
| `found` | command | `review findings`: the findings for the deduplication prompt |
| `dedupe` | agent | marks duplicates: `review dupe` |
| `deduped` | command | `review findings --code`: what is left, with the code around it |
| `skeptic` | agent | a verdict on every finding: `review verdict` |
| `report` | command | `review report`: the YAML report |

Every prompt starts with `{{.Answers.materials}}`: the stages of the review wave share it as the cached prompt prefix.
The state of the review — findings, duplicates, verdicts — lives in the run journal: every `review` command reads it
back with `llamer.results()`.

## Build it in a project

1. Copy this directory without `README.md` into the project, e.g. to `workflows/review/`, and commit it: the review
   takes uncommitted and untracked files as part of the change.
2. Make sure `.llamer.json` exists at the repository root: `{"version": 2}` is enough for `claude`. A `codex` stage
   also needs `drivers.codex.readable` — see the `llamer` skill.
3. Pick `driver`, `model` and `effort` per stage in `workflow.json`. A stage may run on another driver: the prompts
   and commands do not depend on it. The same stage on two drivers is two stages with their own names in one wave
   (`logic`, `logic_codex`).
4. Project knowledge goes into the prompts and into `skills/`: a skill named in a stage's `skills` is offered to it.
   Project stages (`docs`, `migrations`) are copies of `prompts/quality.md` with their own task, added to the wave.

## Run it

| What to review | `REVIEW_BASE` |
|---|---|
| uncommitted changes, untracked files included | `HEAD` |
| the branch against its target, with uncommitted | `$(git merge-base origin/main HEAD)` |
| a commit, on a clean tree checked out at it | `HEAD^` |

```bash
REVIEW_BASE=HEAD REVIEW_TASK="$(cat task.md)" llamer run workflows/review > run.jsonl
tail -n 1 run.jsonl | jq -r '.stages[] | select(.name == "report") | .answer'
```

`REVIEW_TASK` is the text of the task the change implements; set it empty when there is none. The commands need
`git` and `cat` (`commands.programs`).

## Commands

The agent sees them in `review --help`; every one checks its input and refuses with the reason.

| Command | Writes | What it does |
|---|---|---|
| `review materials --base <rev> --task <text>` | `materials` | the shared prompt part; records the base revision for the rest |
| `review assess --nature --risk --level --reason` | `assess` | the verdict on the change |
| `review finding --file --line --line-end --text --severity --severity-reason --related [--suggestion] [--deleted] [--trace]` | `finding` | a remark anchored to changed lines: both ends must be added lines, or removed ones with `--deleted`; a suggestion must change the fragment |
| `review summary --text` | `summary` | the stage's conclusion; a second call replaces it |
| `review stop --reason` | `stop` | the stage gave up: a gap in the report |
| `review list` | — | every finding with its number and state |
| `review diff [path...]` | — | the numbered diff of files, or the file list |
| `review dupe --id --of --reason` | `dupe` | a duplicate drops out |
| `review verdict --id --refuted --reason [--severity --severity-reason]` | `verdict` | a refuted finding drops out |
| `review findings [--code]` | — | the live findings, for a prompt |
| `review report` | — | the YAML report |

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
