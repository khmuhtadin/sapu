# Sapu plugin for Claude Code

Adds the read-only Sapu MCP server and a skill that teaches Claude to use it safely.
Sapu is a macOS cleaner that never loses your data.

```
/plugin marketplace add khmuhtadin/sapu
/plugin install sapu@sapu
```

The server runs `npx -y @khmuhtadin/sapu mcp`, so Sapu does not need to be installed first. Node.js is enough.
Then ask "where did my disk space go?" or "prepare a safe cleanup plan".

Claude can look and prepare a plan. It never applies one: you run `sapu apply <plan>` yourself, and
everything goes to a quarantine that `sapu undo` restores.

More: https://github.com/khmuhtadin/sapu/blob/main/docs/mcp.md
