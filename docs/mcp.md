# Sapu for AI agents (MCP)

`sapu mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server on stdio. It lets an AI agent
such as Claude Code, Codex or Cursor ask Sapu where your disk space went, look inside folders, and prepare a
cleanup plan for you to review.

**The server is read-only. Sapu never deletes anything through MCP.** No tool deletes, moves, undoes, empties
or runs anything. The plan tools write one locked plan file and tell you the command to apply it. You run that
command yourself.

## Add Sapu to your agent

There are three ways. All of them run the same read-only server.

### With npx (nothing to install first)

Needs Node.js. `npx` fetches the `sapu` package, which holds the released binary and nothing else
(no install scripts, no download at run time).

**Claude Code**

```sh
claude mcp add sapu -- npx -y @khmuhtadin/sapu mcp
```

**Codex**

```sh
codex mcp add sapu -- npx -y @khmuhtadin/sapu mcp
```

or in `~/.codex/config.toml`:

```toml
[mcp_servers.sapu]
command = "npx"
args = ["-y", "@khmuhtadin/sapu", "mcp"]
```

**Cursor**

Add this to `~/.cursor/mcp.json` (all projects) or `.cursor/mcp.json` (one project):

```json
{
  "mcpServers": {
    "sapu": { "command": "npx", "args": ["-y", "@khmuhtadin/sapu", "mcp"] }
  }
}
```

A plan's result ends with `sapu apply <plan>`. If you only use `npx`, run it as `npx -y @khmuhtadin/sapu apply <plan>`.

### With Sapu on your `PATH`

Install Sapu first (see the README), so that `sapu` is on your `PATH`.

```sh
claude mcp add sapu -- sapu mcp     # Claude Code
codex mcp add sapu -- sapu mcp      # Codex
```

For Codex's `config.toml` use `command = "sapu"` and `args = ["mcp"]`. For Cursor use
`{ "command": "sapu", "args": ["mcp"] }`.

### As a Claude Code plugin

```
/plugin marketplace add khmuhtadin/sapu
/plugin install sapu@sapu
```

The plugin adds the server (it runs `npx -y @khmuhtadin/sapu mcp`) and a skill. The skill teaches Claude which tool
answers which question, never to run `sapu apply`, `sapu undo`, `sapu quarantine empty` or anything with
`--yes`, and to hand you the `sapu apply <plan>` command to run yourself.

Then ask your agent something like "where did my disk space go?" or "prepare a safe cleanup plan".

## Ask your agent

Copy this into Claude Code. It connects Sapu and keeps you in charge. For Codex or Cursor, change the
first command to the one in the section above.

```text
Set up Sapu (https://github.com/khmuhtadin/sapu), a macOS cleaner, as an MCP
server: run `claude mcp add sapu -- npx -y @khmuhtadin/sapu mcp`, then reload so its tools
show up. Sapu's server is read-only.

Use only the Sapu MCP tools. Call summary and explain, then analyze the biggest
folders, and tell me where my disk space went. Then prepare a safe cleanup plan
with plan_clean (safe only), and plan_purge for my project folders if I name
them. Tell me what is in each plan, in sizes, and what was left alone.

Do not apply the plan. Never run `sapu apply`, `sapu undo`,
`sapu quarantine empty` or anything with --yes, and do not delete files
yourself. Finish by giving me the exact `sapu apply <plan>` command to run in
my own terminal. Everything goes to a quarantine that `sapu undo` restores.
```

## Tools

| Tool | Arguments | What it does |
|---|---|---|
| `summary` | none | Free space, reclaimable bytes by risk, protected items, Full Disk Access status. |
| `explain` | none | What fills macOS "System Data", with the size of each part and the official way to reclaim it. Commands are shown, never run. |
| `analyze` | `path?`, `top?` (1–100, default 20), `depth?` (1–4, default 2) | The largest folders and files under a path. Default: your home folder. |
| `plan_clean` | `max_risk?`: `safe` (default) or `moderate` | Writes a locked plan of caches and logs that apps rebuild. Returns the items by rule, what is not touched, and the command for you. |
| `plan_purge` | `paths` (required), `older_than?` (e.g. `30d`), `max_risk?` | Same for build artifacts (`node_modules`, `target`, ...) inside the project folders you name. |
| `history` | none | Past runs: id, kind, time, items, size, status. |
| `quarantine` | none | What is held in quarantine, with sizes and the earliest time each run may expire. |

Every result has a readable summary in `content` and the same facts as JSON in `structuredContent`.
Errors come back as results with `isError: true` and a plain message. Arguments that do not fit a tool's
schema (a wrong type, an unknown field) are JSON-RPC `-32602` errors.

`risky` is not accepted: `plan_clean` and `plan_purge` refuse `max_risk: "risky"`. Official cleanup commands
(`brew cleanup` and the like) are never part of an MCP plan.

## Safety model

- **Read-only.** The server has no tool that changes anything on disk except `plan_clean` and `plan_purge`,
  and those write exactly one file: `~/.sapu/plans/<timestamp>.json` (or under `$SAPU_HOME`), folder `0700`,
  file `0600`. They never replace an existing file and never write through a symlink. A plan with nothing
  selected writes no file.
- **A human applies the plan.** The result ends with the command, for example
  `sapu apply ~/.sapu/plans/20261108-101500.123.json`. `sapu apply` shows the plan, asks you to confirm, and
  moves the items to quarantine, where `sapu undo` can bring them back. The tools cannot confirm for you, and
  nothing in the protocol takes a `--yes`-like argument.
- **The same gatekeepers as the CLI.** Plans are built by the same engine as `sapu clean --plan`. Protected
  folders (editor data, AI tool data, Mail, Notes, Messages, and so on), whitelisted paths and files that an
  app has open are never selected. When `sapu apply` runs, it checks every item again.
- **Nothing leaves your Mac.** The server talks only to the agent that started it, on stdin and stdout. It
  makes no network calls. Be aware that what a tool returns, such as folder names from `analyze`, goes to
  your agent and from there to the model it uses.
- **Plan files are data you review.** Look at the plan `sapu apply` shows before you confirm, as you would for
  any plan. An agent that can edit files could change a plan file before you apply it; `sapu apply` still
  refuses protected, whitelisted and in-use items, and skips anything that changed since planning, but the
  confirmation is yours.

## Protocol notes

- Transport: stdio, one JSON-RPC 2.0 message per line. Protocol revision `2025-06-18`. stdout carries only
  protocol messages; logs go to stderr. EOF on stdin ends the server after running tool calls have answered.
- Methods: `initialize`, `notifications/initialized`, `ping`, `tools/list`, `tools/call`. Unknown methods get
  `-32601`.
- `notifications/cancelled` stops a running tool call, which then sends no reply.
- JSON-RPC batches are not supported (the 2025-06-18 revision removed them).
