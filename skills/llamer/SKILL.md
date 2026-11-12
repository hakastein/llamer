---
name: llamer
description: Use when reviewing changes with LLaMer — uncommitted, staged, unpushed, a branch, a commit range or a merge request.
---

`llamer` runs an agentic review of a set of changes and prints the result as one YAML document on stdout; the log goes to stderr. It finds nothing by itself: what to review, from where and against which task comes in arguments.

| What to review | Arguments |
|---|---|
| uncommitted changes, untracked files included | `--base HEAD --head worktree` |
| staged changes | `--base HEAD --head index` |
| unpushed commits | `--base @{u} --head HEAD` |
| the branch against its target, committed | `--exclude origin/<target> --head HEAD` |
| the branch against its target, with uncommitted | `--exclude origin/<target> --head worktree` |
| a commit | `--base <sha>^ --head <sha>` |
| a range | `--base <from> --head <to>` |
| a merge request, nothing published | `--mr <url> --dry-run` |

- `--exclude` repeats: add `--exclude <shared branch>` for every branch merged into this one whose code passed its own review (`--exclude origin/develop`). Everything reachable from an excluded revision stays out, merges included. It does not combine with `--base`.
- Revisions are taken as they are: `git fetch` the target and shared branches first. A shallow clone fails with no common history.
- The task text is yours to fetch — from the tracker, the MR description, the user: write it to a file outside the tree and pass `--task <file>`. Without it the review has no task to check the implementation against.
- `--preflight` checks drivers, their login and skills, and prints YAML `gaps` without starting the run.

## Personal config

`.llamer.local.json` next to `.llamer.json` (or next to the `--config` file) is a JSON Merge Patch over it: objects merge, arrays and values replace, `null` removes a key. It stays out of the repository. Only `claude` installed, every stage on it, cheaper:

```json
{ "driver": "claude", "stages": { "logic": { "drivers": null, "level": 2 }, "quality": { "drivers": null } } }
```

`null` drops a stage's own `drivers`, so the stage falls back to the root `driver`. `level` is `1`–`5` or `"assessed"` (depth chosen by the assessor); a lower number is a cheaper model.

## The result

| Field | What it is |
|---|---|
| `ok` | `false` — the run broke: `failure.reason`, `failure.text`; exit code 1 |
| `findings[]` | `file`, `line`, `lineEnd`, `deleted` (`true` — old side of the diff), `severity` (`minor`/`major`/`critical`), `text`, `suggestion`, `stage`, `driver` |
| `assessment` | `nature`, `risk`, `level`, `reason`; `null` when there was nothing to review |
| `summaries[]` | each reviewing stage's conclusion |
| `resolvedThreads[]` | with `--mr`: threads the run would close and why |
| `gaps[]` | what the run did not cover: failed or stopped stages, findings left unchecked; any gap makes exit code 1 |
| `notices[]` | a stage instance that failed while its stage ran on another driver: reported, the run still passes |
| `cost` | `usd`, tokens, duration |
| `limits` | subscription limits per driver, also on failure: `windows` (`five_hour`, `seven_day`) with `usedPercent` and `resetsAt` (unix seconds), `status` `allowed`/`allowed_warning`/`rejected`, `reason`; a driver that reported nothing is absent |
| `stages[]` | `name`, `model`, `ok`, `usd`, `resume` |
| `report` | `report.md`, the same for a human |
| `artifacts` | the run directory: prompts, journals, `task.diff`, sessions |

To ask a stage why it did something, run its `resume` command with the question appended in single quotes: `<resume> 'why did you skip src/Order.php?'`.
