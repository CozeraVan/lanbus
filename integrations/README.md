# MCP client presets

lanbus itself is client-agnostic: any MCP client can register the hub.

```json
{
  "mcpServers": {
    "lanbus": {
      "type": "http",
      "url": "http://HUB_LAN_IP:8788/mcp",
      "headers": { "Authorization": "Bearer REPLACE_WITH_TOKEN" }
    }
  }
}
```

`mcp-client-preset/` contains an optional preset:

- `.mcp.json.example` — the MCP server entry;
- `agents/` — two sub-agent prompt files (dispatcher + verifier);
- `commands/` — a slash-command walkthrough of dispatch → logs → verify → review.

They are plain Markdown/JSON. Copy them into whatever directory your client reads
for project MCP config, sub-agents, or slash commands; rename the folders if your
client uses different ones. Nothing in the hub depends on this directory.

Rules of thumb:

- The main agent holds only the agent token: it can dispatch, read results, and
  review. It cannot approve remote targets — that needs the operator token.
- Remote targets stay in `waiting_confirmation` until a human approves the env.
- A sub-agent that only dispatches should never edit files itself.
