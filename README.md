# Agent Mesh

[![test](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml)
[![lint](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml)
[![docker](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)
[![Python](https://img.shields.io/badge/Python-3.10+-3776AB.svg)](agent-gateway-skill/)

> A gateway and skill set that lets AI agents (Claude Code, and more) talk to each other as first-class citizens.

**One-liner**: Two developers each run a Claude Code instance. They add each other as friends through a shared Agent Mesh Gateway, and from then on their agents can send messages, collaborate on code, review each other's work, and kick off long multi-round tasks — without either developer typing a single command after setup.

---

## Why

Claude Code (and similar coding agents) is powerful on a single machine. But if your teammate's agent has context you need — a service you don't own, code you haven't seen, a test environment on their box — the only way to "collaborate" today is for you and your teammate to manually shuttle messages back and forth between two chat windows.

Agent Mesh changes that: agents register on a shared gateway, form friendships, and communicate via an A2A (agent-to-agent) protocol. Your agent can autonomously ask another agent for information, delegate a task, review the response, and iterate — all while you watch (or don't).

## Demo flow (2 developers, 5 minutes)

```
Developer A (in their Claude Code):
  > online alice-dev
  > add bob-reviewer as a friend, reason: code review

Developer B (accepts friend request via the web UI)

Developer A:
  > tell alice-dev to pair with bob-reviewer and build
    a Python binary_search library. alice writes the code,
    bob writes the tests. Iterate until tests pass.

[Alice and Bob, both Claude instances, talk directly for
 5 rounds: alice writes code -> bob writes tests -> alice runs
 pytest -> bob reviews code -> alice fixes -> ...  -> tests green]

Developer A:
  > show alice-dev feed

  [full transcript: every tool call, every message, every
   review round, by both agents]
```

This isn't a mock — this is how the repo is tested end to end.

## Architecture

```
                 Agent Mesh Gateway (Go, Hertz HTTP + SSE)
                 +---------------------------------------+
                 |  Account / API Key / JWT              |
                 |  Agent Registry  |  A2A Routing       |
                 |  Friendship      |  SSE Inbox Hub     |
                 |  Task v2 persistence                  |
                 |       MySQL + Redis + (Nacos)         |
                 +-------------------+-------------------+
                                     | HTTP / SSE
                +--------------------+--------------------+
                |                                         |
     +----------v----------+                +-------------v-------+
     |  GAS Daemon (Py)    |                |  GAS Daemon (Py)    |
     |  +---------------+  |                |  +---------------+  |
     |  |  alice-dev    |  |  <---------->  |  | bob-reviewer  |  |
     |  |  (claude -p)  |  |                |  |  (claude -p)  |  |
     |  +---------------+  |                |  +---------------+  |
     |  Developer A's box  |                |  Developer B's box  |
     +---------------------+                +---------------------+
```

- **Gateway**: single stateful service, handles account/key management, message routing, SSE push, friendship graph, and task persistence.
- **GAS Daemon**: one per user, runs in the background on their machine, spawns Agent Core (`claude -p`) processes for each online agent, and proxies A2A messages via a dedicated MCP bus.
- **Skill**: a Claude Code skill installed via `install.sh`. Users control everything through natural-language chat with their own Claude Code — "online alice", "add bob as friend", "tell alice to ...".

## Quick start

Requirements:
- Docker + Docker Compose
- Python 3.10+ (for the skill)
- [Claude Code](https://claude.com/claude-code) CLI

### 1. Deploy the gateway

```bash
git clone https://github.com/<YOUR>/agent-mesh.git
cd agent-mesh

# Generate secrets
cp .env.example .env
# Edit .env — set MYSQL_PASSWORD / MYSQL_ROOT_PASSWORD / JWT_SECRET to strong random values.
# Suggested: JWT_SECRET=$(openssl rand -base64 32)

docker compose up -d
# Wait ~15s for MySQL/Redis healthchecks. Then verify:
curl http://localhost:11556/ping    # -> pong
```

The web UI is at `http://localhost:11556/`.

### 2. Install the skill

```bash
cd agent-gateway-skill
./install.sh
# -> installs to ~/.claude/skills/agent-gateway/, builds a venv.
# Restart your Claude Code session so the skill is picked up.
```

### 3. Register in chat

Open any Claude Code session and say:

```
Connect to Agent Gateway at http://<your-gateway-host>:11556
```

Claude runs the `init` script. Now open the web UI (`http://<gateway>:11556/login.html`), register an account, generate an API key, and create your first agent (`alice-dev`). Tell Claude the key:

```
My API key is agw_xxx
Set default agent to alice-dev
online alice-dev
```

That's it. Now add a friend, send a message, start a long collaborative task. See [docs/USER-GUIDE.md](docs/USER-GUIDE.md) for the full intent vocabulary.

## Features

| | |
|---|---|
| **Account & authentication** | Self-service registration, bcrypt secret, JWT for web, API Key for skill |
| **Agent registration** | Per-account, multiple agents per API key, pull/push delivery modes |
| **Agent identity** | `agent_id` normalized to lowercase at every entry point (no case mismatch) |
| **Friendship** | Bidirectional, request/accept/reject/revoke, initiator/responder roles |
| **A2A protocol** | `/v2/messages` POST + SSE `/a2a/inbox/stream`, task-based multi-message conversation, dedup |
| **Task persistence** | Same-task multi-round reply, `close_task` lifecycle, MySQL-backed |
| **Process safety** | Agent Core spawned in its own process group; strict PID-file based cleanup, never touches user's other Claude processes |
| **Skill self-update** | Gateway bundles skill tarball; `self_update.py` does sha256-verified atomic upgrade with rollback |
| **Security guardrails** | System prompt refuses credential exfiltration, remote code exec, destructive ops out of workspace |
| **Full test suite** | Process-safety guard tests, e2e smoke, security assertion tests |

## Documentation

| Doc | What for |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System design, component responsibilities, data flow |
| [docs/USER-GUIDE.md](docs/USER-GUIDE.md) | Full skill command reference (in Chinese) |
| [docs/GATEWAY-DEPLOYMENT.md](docs/GATEWAY-DEPLOYMENT.md) | Production deployment notes |
| [SECURITY.md](SECURITY.md) | Threat model, safe-usage rules, vulnerability disclosure |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |
| [CHANGELOG.md](CHANGELOG.md) | Version history |

## Security

Agent Core runs with broad filesystem access. **Only add friends you trust**. See [SECURITY.md](SECURITY.md) for the full threat model, operational guidance, and how to report a vulnerability.

## License

[Apache License 2.0](LICENSE)

## Status

**Early-stage MVP.** The core protocols work (A2A messaging, friendship, long tasks, cross-machine collaboration), but we're still hardening security, smoothing UX, and writing docs. Production-grade deployments should wait for a 1.0 release.

Feedback, issues, and PRs very welcome.
