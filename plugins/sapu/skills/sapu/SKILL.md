---
name: sapu
description: Find where a Mac's disk space went and prepare a safe cleanup plan with Sapu. Use when the user asks about disk space, "System Data", a full disk, caches, node_modules or build artifacts, or wants to clean up their Mac. Read-only: it never applies a plan.
---

# Sapu: disk space on a Mac

Sapu is a macOS cleaner that never loses user data. Its MCP server (`sapu`) is read-only. You look,
explain and prepare a plan. The user applies it.

## Use the MCP tools

Prefer the tools over shell commands. Pick by the question:

| Question | Tool |
|---|---|
| "How full is my disk? What can go?" | `summary` |
| "What is System Data?" | `explain` |
| "What is big in this folder?" | `analyze` (`path`, `top`, `depth`) |
| "Clean up caches and logs" | `plan_clean` (`max_risk`: `safe`, the default, or `moderate`) |
| "Clean old node_modules, target, ..." | `plan_purge` (`paths` required, `older_than` such as `30d`) |
| "What did Sapu do before?" | `history` |
| "What is held in quarantine?" | `quarantine` |

Start with `summary` or `explain`, then `analyze` where the space is. Use `plan_clean` or `plan_purge`
only when the user wants to clean. Start with `safe`. Offer `moderate` only if the user asks for more,
and say what it adds. Tell the user what a plan contains, in sizes and categories, and what was left
alone and why.

## Never apply anything

Never run any of these, and never run anything with `--yes`, `--pick` or `--run-commands`:

- `sapu apply`
- `sapu undo`
- `sapu quarantine empty` or `sapu quarantine expire`
- `sapu clean`, `purge`, `uninstall`, `trash`, `installer`, `dupes`, `packages`, `optimize`

Do not edit a plan file. Do not pipe an answer into a Sapu prompt. Do not delete files yourself to "help".
If the user asks you to apply a plan, say you cannot, and give them the command.

## Hand over the command

A plan tool ends with a command. Give it to the user exactly as printed, to run in their own terminal:

```sh
sapu apply ~/.sapu/plans/20261108-101500.123.json
```

If `sapu` is not on their `PATH`, they can use `npx -y @khmuhtadin/sapu apply <plan>`. Tell them `sapu apply` shows the
plan and asks for confirmation before it moves anything.

## What to tell the user about safety

- Nothing is deleted straight away. `sapu apply` moves items to a quarantine, and `sapu undo` restores
  them. The `quarantine` tool shows what is held.
- Protected folders (editor data, AI tool data, Mail, Notes, Messages), whitelisted paths and files an
  app has open are never selected. `sapu apply` checks every item again.
- When in doubt Sapu skips an item and says why. Do not suggest ways around a skip.
- Folder names that `analyze` returns go to you and to the model. Mention that if the user is
  concerned about privacy.
