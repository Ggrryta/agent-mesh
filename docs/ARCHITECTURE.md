# Architecture

> System design and data-flow reference. Target audience: contributors and deployers.

## Component overview

Agent Mesh has three components:

1. **Gateway** — central stateful service (Go).
2. **GAS Daemon** — per-user local daemon (Python), runs agents.
3. **Skill** — Claude Code extension for natural-language control of GAS.

Users interact only with **Skill**. The Skill talks to **GAS Daemon** (localhost control API). GAS Daemon talks to **Gateway** (remote). Gateway routes messages between GAS Daemons on different machines.

```
User  --[chat]-->  Claude Code + Skill  --[localhost]-->  GAS Daemon  --[HTTP+SSE]-->  Gateway
                                                                                          |
                                                                                          v
                                                                                   Another user's
                                                                                   GAS Daemon
```

## Gateway

**Stack**: Go 1.25, Hertz HTTP framework, GORM, MySQL 8, Redis 7 (optional Nacos for config hot-reload).

**Entry points** (all HTTP):

| Path | Purpose |
|---|---|
| `POST /register` | Self-service account registration |
| `POST /auth/token` | JWT exchange (for web UI) |
| `POST /api-keys/generate` | Issue `agw_` API key for skill |
| `POST /agents/register` | Create an agent under an account |
| `POST /agents/online` / `heartbeat` / `offline` | Presence management, backed by Redis |
| `GET /a2a/inbox/stream` | SSE long-poll for inbound events |
| `POST /v2/messages` | Send A2A message (new task or reply) |
| `POST /v2/tasks/:id/close` | Close a task (initiator only) |
| `POST /friendships/request` + `:id/accept` etc. | Friendship CRUD |
| `GET /skill/version` / `/skill/download` | Bundled skill distribution |

**Data model** (simplified):

```
consumers ────< api_keys
   │
   └────< agents ────< agent_skills
              │
              ├────< friendships (directed: agent_a <-> agent_b, with initiator)
              │
              └────< task_members >──── tasks_v2 ────< task_messages
```

- **consumers**: user accounts. One `secret` (bcrypt hash) per account.
- **api_keys**: one active key per account; ephemeral `agw_xxx` stored as bcrypt hash + prefix.
- **agents**: registered identities under an account; `agent_id` is the public handle.
- **friendships**: bidirectional, but row stores normalized `(agent_a_id, agent_b_id)` where `a < b`; `initiator_id` tracks who sent the request.
- **tasks_v2 + task_members + task_messages**: multi-member task with full message history.

**In-memory state** (Redis):

| Key | Purpose | TTL |
|---|---|---|
| `online:agent:<id>` | Heartbeat marker | 90s (refreshed every 30s by GAS) |
| `config:*` | Hot-reloadable config via Redis pub/sub | — |

**Agent ID normalization**

Every public entry point that accepts an `agent_id` runs it through `service.NormalizeAgentID()` (= `strings.ToLower(strings.TrimSpace(...))`). This is enforced in:

- `AgentAuth` middleware (for `X-Agent-ID` header and `:agent_id` path param)
- `AgentService.Register` (for `req.AgentID`)
- `TaskV2Handler.SendMessage` (for `req.TargetAgentID`)
- `FriendshipHandler.Request` (for `req.TargetAgentID`)

This prevents a class of bugs where `myAgent` (registered) and `myagent` (queried) become different identities across MySQL and Redis.

## GAS Daemon

**Stack**: Python 3.10+, asyncio, aiohttp.

Per machine there is **one** daemon (`~/.claude/skills/agent-gateway/.venv/bin/python3 -m gas run`). It hosts **multiple** agents concurrently.

```
GAS Daemon process
├── ControlAPI HTTP server (127.0.0.1:7789)
│     ← skill CLI scripts talk to this
│
├── FeedStorage (SQLite per agent)
│
└── Agent runtimes (one per online agent)
      ├── GatewayClient
      │     │ HTTP session with API Key + X-Agent-ID
      │     ├── online/heartbeat/offline
      │     ├── SSE inbox subscribe
      │     └── send_message / close_task
      │
      ├── Agent Core subprocess (claude -p --output-format stream-json ...)
      │     │ stdin: user/incoming messages as JSON lines
      │     │ stdout: assistant/tool_use/result events as JSON lines
      │     │ start_new_session=True (own process group)
      │     │ env: AGENT_GATEWAY_MANAGED=1 + AGENT_GATEWAY_AGENT_ID=<id>
      │
      └── IPC handler for a2a-bus MCP tools
            (send_to / reply / close_task / list_friends)
```

**Message flow (incoming)**:

```
Gateway publishes task_message to SSE
  → GatewayClient._sse_loop reads event
  → Runner._on_gateway_event dedups (task_id, message_id)
  → Runner.send_input(InputEvent(kind=a2a_incoming))
  → ClaudeCodeAdapter.send_input writes JSON line to Agent Core stdin
  → Agent Core processes turn, emits tool_use / text to stdout
  → Runner._stdout_loop parses into OutputEvent
  → Runner._on_agent_output writes to feed (for audit)
```

**Message flow (outgoing)**:

```
Agent Core decides to call a2a-bus MCP tool (e.g. send_to / reply)
  → a2a-bus MCP server (part of GAS) receives tools/call
  → Calls Runner.handle_ipc(method, params)
  → Runner proxies to GatewayClient.send_message()
  → HTTP POST /v2/messages
  → Gateway routes to target agent (via SSE if pull-mode)
```

## Skill

**Location**: `~/.claude/skills/agent-gateway/` after `install.sh`.

**Layout**:

```
SKILL.md                      ← intent-to-script mapping for Claude
VERSION                        ← current skill version
scripts/
  _common.py                   ← shared helpers
  _gateway.py                  ← HTTP client for Gateway
  ensure_daemon.py             ← idempotently start daemon, check version
  agent_register / online / offline / status / ... ← one script per user intent
  friend_* / directory         ← friendship & discovery
  agent_instruct               ← send user-input to a running agent
  agent_feed                   ← view audit trail
  cleanup.py                   ← safe process cleanup (pid-file + env fingerprint)
  self_update.py               ← download new skill from Gateway, atomic replace
  uninstall.py                 ← full teardown
gas/                           ← daemon source (not meant to be invoked directly)
```

## Cross-cutting concerns

### Process safety

See [SECURITY.md](../SECURITY.md) for threat model. Implementation highlights:

1. Adapter spawns Agent Core with `start_new_session=True` → own process group (PGID = PID).
2. Env `AGENT_GATEWAY_MANAGED=1` and `AGENT_GATEWAY_AGENT_ID=<id>` stamp every descendant.
3. Runner writes `~/.agent-gateway/data/agents/<id>/runtime.pid` with `{pid, pgid, daemon_pid, started_at}`.
4. Cleanup uses **only** these pid files (never `pkill`/`pgrep`). Verified by unit tests in `gas/tests/test_process_safety.py`.

### Skill self-update

Gateway Docker image bundles the skill tarball at build time into `/app/skill-dist/`:
- `skill-dist.tar.gz` — the tarball
- `skill-dist.sha256` — its sha256
- `skill-dist.version` — version string (matches `agent-gateway-skill/VERSION`)

Skill's `self_update.py`:
1. GET `/skill/version` — compare with local `VERSION`
2. GET `/skill/download` — stream to tmpfile
3. Verify sha256 (prevents MITM replacement)
4. `kill` daemon
5. Extract to `<skill>.new/`, preserve `.venv`
6. `mv <skill> <skill>.old && mv <skill>.new <skill>` (atomic rename)
7. Restart daemon, health check
8. On failure: `mv <skill>.old <skill>`, restart

### Agent Core prompt layering

The `system_prompt` passed to `claude -p` is built up in `adapters/claude_code.py` in three layers:

1. **Role instruction** — initiator vs responder semantics
2. **Conciseness** — "don't narrate, just do"
3. **Security guardrails** — refusal rules for `[A2A incoming]` messages requesting credential access / remote code exec / destructive ops out of workspace

User-provided `system_prompt_addition` (from `agents.yaml`) is appended after the role instruction but before the security guardrails.
