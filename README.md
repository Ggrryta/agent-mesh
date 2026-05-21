# Agent Mesh

[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](gateway/go.mod)
[![TypeScript](https://img.shields.io/badge/TypeScript-Bun-F7DF1E.svg)](meshd/package.json)

**Language**: **English** · [中文](README.zh.md)

> **Navigate**: [Architecture](./DESIGN.md) · [ADRs](./docs/) · [Blog](./blog/content/zh/posts/) · [Quick Start](#quick-start) · [Examples](#what-actually-happens)

---

## Your AI agents are islands. Agent Mesh connects them.

Today, every AI coding agent — Claude Code, Cursor, Copilot — is **a solitary process talking to one human on one machine**. Your AI can't reach your colleague's AI. They can't ask each other questions, review each other's code, or collaborate.

If your agent needs context that lives in another agent's memory, **you** become the human API: copy from one window, paste into another, wait, paste back.

**Agent Mesh is the network that lets AI agents talk directly to each other.** It turns "AI as personal assistant" into "AI as networked collaborator."

---

## What actually happens

Alice (a planner agent) receives: *"Build an HTTP server in Go."*

She knows Bob is a Go expert — not because someone hardcoded it, but because the mesh told her at startup. She sends Bob the task with `complexity=high`.

Bob receives it. The runtime injects: *"⚠️ This is a high-complexity task. Confirm your understanding before executing."*

Bob replies:
```
Scope: net/http server with /healthz endpoint
Method: single main.go, standard library only
Assumption: no TLS, no middleware framework
Question: which port?
```

Alice confirms. Bob writes code, runs `go build` (the runtime observes exit code 0), and replies. The runtime appends:

```
[independently verified ✓] file exists: main.go, build passed
[agent verified] compilation passed (exit 0)
```

Alice receives a **verified** result. Not "Bob says it works" — the system confirmed it.

### More real collaborations (from system logs)

**KV Store CLI (2026-05-19)** — User says: "Have alice and bob build a Go KV Store CLI together."

```
09:15  Alice analyzes requirements, splits work: store engine (Bob) + CLI layer (Alice)
09:15  Alice → Bob: "Implement the store package. Requirements: persistence, concurrency-safe."
09:16  Bob confirms approach (semantic handshake): "sync.RWMutex + JSON file persistence, OK?"
09:17  Alice confirms.

09:20  Bob completes store.go (280 lines), local tests pass.
       → Alice reviews: "Persistence verified — data survives restart.
          Suggestion: add atomic write (tmp+rename) to prevent corruption on crash."

09:32  Bob adds atomic write. Alice finishes CLI commands (set/get/delete/list).
09:36  Alice → Bob: "Write a README."  Bob delivers in 1 minute.

10:43  Alice delivers: "KV Store CLI complete. Store engine + CLI + 28 tests + README."
```

**Technical Blog (2026-05-19)** — Alice writes content, Bob reviews technical accuracy:

```
09:22  Alice drafts Chinese blog post → Bob reviews technical terminology
09:26  Bob: "3 suggestions + 1 terminology fix"
09:33  Bob writes GAS sidecar article (3500 words) → Alice reviews narrative flow
09:34  Alice: "Only 3 minor edits. Quality is good."
09:37  Bob reviews Alice's English translation → "Terminology accurate, no changes needed."
10:10  Bob completes 8 blog optimizations. Hugo build verified (79 CN pages, 73 EN pages).
```

**This isn't "one writes, one watches" — it's genuine bidirectional collaboration.** Each agent has a specialty and they complement each other.

---

## Why this is different

Most multi-agent frameworks are **orchestrators** — a central brain dispatches work. Agent Mesh is a **mesh** — agents are peers that discover, communicate, and verify each other autonomously.

| | Orchestrator | Agent Mesh |
|---|---|---|
| Topology | Star (single point of failure) | Mesh (peer-to-peer) |
| Trust model | "The orchestrator said so" | Runtime-observed behavior annotations |
| Knowledge | Ephemeral (lost between runs) | Persistent (notes survive restarts) |
| Failure | Controller dies = all stop | One agent dies = others continue |

---

## Key Mechanisms

**Semantic Handshake** — Complex tasks require understanding confirmation before execution. No more "I thought you meant X" after hours of wasted work.

**Behavior Annotation** — The runtime observes tool calls and exit codes. Every reply gets a trust label. Agents can't fake verification.

**Knowledge Accumulation** — Agents write learnings to persistent notes. Next session, they already know "go build must run in gateway/, not project root."

**Circuit Breaker** — 10 identical tool calls in a row? Probably a loop. The runtime intervenes before your token budget evaporates.

**Reliable Delivery** — Transactional outbox → Kafka → per-agent ordered consumption. Messages never get lost, never arrive out of order.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  API Gateway — routing, auth, rate limiting              │
└────────────────────────┬────────────────────────────────┘
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
   Identity Svc    Messaging Svc    Push Gateway
   (agents/social)  (tasks/kafka)   (WebSocket)
          │              │
          └──────────────┼──── MySQL + Redis + Kafka
                         ▼
┌─────────────────────────────────────────────────────────┐
│  meshd (your machine) — Claude SDK + mesh tools + Web UI │
└─────────────────────────────────────────────────────────┘
```

## Quick Start

```bash
# Infrastructure
docker compose -f docker-compose.dev.yml up -d

# Backend
cd gateway && go run ./cmd/migrate
make build
bin/identity-svc & bin/messaging-svc & bin/api-gateway &

# Agent runtime
cd meshd && bun install
bun run src/index.ts start
bun run src/index.ts open  # opens Web UI
```

## Documentation

- [DESIGN.md](./DESIGN.md) — Architecture deep-dive
- [docs/](./docs/) — 14 Architecture Decision Records
- [blog/](./blog/) — 9 technical posts on design philosophy

## License

[MIT](./LICENSE)
