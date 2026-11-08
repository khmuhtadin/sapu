# sapu

Sapu is a macOS cleaner for the command line. It finds caches, logs, build
artifacts and leftovers, shows you what it would remove and why, and moves
what you pick to a quarantine. `sapu undo` puts it back.

```sh
npx @khmuhtadin/sapu            # the menu: free space, cleanup, quarantine, history
npx @khmuhtadin/sapu explain    # what fills "System Data"
npx @khmuhtadin/sapu analyze    # the biggest folders in your home folder
```

Install it for good with `npm install -g @khmuhtadin/sapu`, then run `sapu`.
Sapu needs macOS 13 or later.

## For AI agents

`sapu mcp` is a read-only [Model Context Protocol](https://modelcontextprotocol.io)
server. Your agent can ask where your disk space went and prepare a cleanup
plan, and you apply the plan yourself.

```sh
claude mcp add sapu -- npx -y @khmuhtadin/sapu mcp     # Claude Code
codex mcp add sapu -- npx -y @khmuhtadin/sapu mcp      # Codex
```

Claude Code also has a plugin that adds the server and a skill:
`/plugin marketplace add khmuhtadin/sapu`, then `/plugin install sapu@sapu`.

## The safety promise

Sapu's whole value is that it never loses your data. Nothing is deleted
straight away: `sapu apply` moves items to a quarantine and `sapu undo`
restores them. Protected folders (editor data, AI tool data, Mail, Notes,
Messages and others), whitelisted paths and files an app has open are never
touched. When in doubt, Sapu skips the item and tells you why. The MCP server
has no tool that deletes, moves or undoes anything.

## What is in this package

The binary is the same file as the one in the
[GitHub release](https://github.com/khmuhtadin/sapu/releases) of the same
version: the universal (Apple Silicon and Intel) build, taken from the release
archive after its SHA-256 was checked against the release's `checksums.txt`.
The package has no install scripts and downloads nothing, at install time or
at run time. It does nothing on other systems than macOS.

0.x releases are developer previews: not signed with an Apple Developer ID and
not notarized. [docs/verify.md](https://github.com/khmuhtadin/sapu/blob/main/docs/verify.md)
shows how to check the checksum and the cosign signature.

## Links

- Source and issues: https://github.com/khmuhtadin/sapu
- Verify a download: https://github.com/khmuhtadin/sapu/blob/main/docs/verify.md
- Use with AI agents: https://github.com/khmuhtadin/sapu/blob/main/docs/mcp.md
- Website: https://sapu.khaisa.studio

Apache-2.0.
