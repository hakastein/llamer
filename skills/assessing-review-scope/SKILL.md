---
name: assessing-review-scope
description: Use when deciding a merge request's nature of change, risk and the depth of the review to come
---

# Assessing review scope

## Overview

Two calls on a request before anyone reads it closely: does behaviour change,
and what would a missed defect cost.

## Nature

A mass run of one pattern — cs-fixer, rector, a regenerated file — is
`mechanical` however many files it touches. One added `return` is `behavioral`.
The author's description is not evidence; the hunks are.

## Level

One axis: the cost of a defect the review misses. Not volume, not the number of
files.

The top of the scale is taken by irreversibility — data lost or silently
corrupted, a critical flow broken: money, transactions.
Three lines are enough. A defect that will be noticed and fixed stays below,
however many modules it crosses.

The level is set by the most expensive hunk, not averaged across the request.

## Common Mistakes

- Raising the level because the request is large, or lowering it because the
  request is small.
- Calling a request `mechanical` because the author called it a refactor, while
  one hunk changes a condition.
