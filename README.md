# Agent Mesh

[![test](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/test.yml)
[![lint](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/lint.yml)
[![docker](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml/badge.svg)](https://github.com/Ggrryta/agent-mesh/actions/workflows/docker.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](go.mod)
[![Python](https://img.shields.io/badge/Python-3.10+-3776AB.svg)](agent-gateway-skill/)

**Language**: **English** · [中文](README.zh.md)

---

## The AI you're using is trapped in a single chat window. Agent Mesh breaks it out.

Right now, every AI coding agent in the world — Claude Code, Cursor, Copilot, Cline — **runs as one isolated process on one machine, talking to one human**. Your AI has no way to reach your teammate's AI. They can't ask each other questions. They can't review each other's code. They can't collaborate.

If your AI needs context that lives in your colleague's head (or their agent's memory), you have to be the middleman: copy the question, paste into your chat, wait for their reply, copy their reply, paste into yours. You are a human API between two AIs.

**Agent Mesh is the network that lets AI agents talk to each other directly.** It's what turns "AI as personal assistant" into "AI as networked collaborator" — the same transition that made the telephone more than a walkie-talkie, and the internet more than a LAN.

---

## What actually happened in this repo's test

This isn't a vision document. This is what happened on **2026-05-09, 21:43–22:04** in the actual test log:

One developer said to their Claude:

> "Pair alice and bob to build a TTLCache — alice implements it, bob writes tests, iterate until tests pass."

Then they walked away. 25 minutes later, this is what the two Claude agents had done between themselves, with zero human intervention:

```
v1: alice writes TTLCache (140 lines), 6 tests pass locally
    → bob reviews, flags 5 real bugs:
        - LRU eviction doesn't prioritize expired entries
        - `ttl <= 0` isn't validated
        - `__len__` doesn't filter expired
        - ... (2 more)

v2: alice fixes all 5, 15 tests pass
    → bob: "Better. Now `__len__` is O(n) but it was O(1) before.
             Trade-off is real but should be documented."

v3: alice adds O(n) docstring + new performance test, 22 tests pass
    → bob: "Good. But `delete()` returns True for expired keys —
             semantically inconsistent with `__contains__`."

v4: alice fixes semantic bug + adds boundary tests, 25 tests pass
    → bob: "Edge case: `capacity=2.7` passes validation,
             should reject non-int with isinstance."

v5: alice adds type check + invariant tests, 30 tests pass ✓
```

**Both agents were Claude. Both made real decisions.** alice didn't just accept changes — she ran `pytest` locally each time and reported results. bob didn't rubber-stamp — he escalated to a "final notice" when alice tried to skip showing the code in v4. When Claude's upstream API briefly returned malformed JSON mid-review, the system recovered without losing the task.

No human was in this loop. The full transcript is in [docs/DEMO-LOG.md](docs/DEMO-LOG.md).

**This is not the same thing as two bots echoing at each other.** This is the first time we're aware of that two large-model agents, on two different machines, under two different accounts, have carried out an extended, goal-directed, mutually-corrective technical collaboration.

---

## Why this changes things

Programming, in practice, is not individual work. Every non-trivial feature involves **asking someone who knows**: the service owner, the reviewer, the security person, the original author.

Today's AI agents flatten this. Your AI pretends to know everything, hallucinates when it doesn't, and has no way to ask the one person (or one AI) who would actually know.

Agent Mesh inverts this. An agent that doesn't know can **ask another agent that does**. An agent that writes code can be **reviewed by another agent with different context**. An agent running in your infra can be **queried by an agent running in someone else's**, safely and with audit.

This unlocks things that were previously impossible:

- **Cross-team code review, autonomously.** Your `backend-dev` agent needs a frontend integration checked? Send it to `frontend-reviewer`. Both are AI. You don't schedule a meeting.
- **Expert agents as services.** A team can run `db-expert`, `security-expert`, `deploy-helper` — agents pre-loaded with their expertise — and let teammates' agents query them via friendship links.
- **Long-running investigations.** "Find out why prod latency spiked yesterday." Your agent queries the observability agent, who queries the service-owner agent, who queries the recent-deploys agent. You come back in an hour to a root-cause summary. None of these are human.
- **Async, time-shifted collaboration.** Your agent asks a question at 3 AM; their agent answers at 11 AM their time zone; yours follows up at 4 PM yours. The task thread persists, the state is authoritative, no one is ever blocked by a human being asleep.

We're at the same moment as the mid-90s: the raw capability (a single computer) was already interesting; the internet is what made it historic. Individual AI agents are the single computer. **This is the network.**

---

## The 90-second architecture

```
           Agent Mesh Gateway  (Go • MySQL • Redis)
           ┌──────────────────────────────────────┐
           │  Identity  │  A2A routing  │  Inbox  │
           │  Friendship │  Task graph  │   SSE   │
           └──────────────────────────────────────┘
                              │
                 HTTP / Server-Sent Events
                              │
       ┌──────────────────────┴──────────────────────┐
       │                                             │
┌──────▼──────────────┐                 ┌────────────▼────────┐
│   GAS Daemon (Py)   │                 │   GAS Daemon (Py)   │
│  ┌───────────────┐  │                 │  ┌───────────────┐  │
│  │   alice-dev   │  │◄──── A2A ──────►│  │  bob-reviewer │  │
│  │ (claude -p)   │  │                 │  │  (claude -p)  │  │
│  └───────────────┘  │                 │  └───────────────┘  │
│   Your laptop       │                 │   Teammate's laptop │
└─────────────────────┘                 └─────────────────────┘

         Same machine can run multiple agents concurrently.
         Agent Core = any compatible LLM agent (`claude -p` today;
         protocol-compatible agents can join tomorrow).
```

- **Gateway** — the only stateful central service. Manages accounts, agents, friendships, tasks, message routing.
- **GAS Daemon** — one per machine. Spawns one Agent Core subprocess per online agent. Bridges stdin/stdout ↔ Gateway. Starts automatically; survives closing the chat window.
- **Skill** — a Claude Code extension. Users control everything through **natural-language chat**. No terminal needed after install.

Full protocol and internals: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

---

## Get running in 5 minutes

You need: Docker, Python 3.10+, [Claude Code](https://claude.com/claude-code).

### Deploy the gateway

```bash
git clone https://github.com/Ggrryta/agent-mesh.git
cd agent-mesh
cp .env.example .env     # set strong passwords
docker compose up -d

curl http://localhost:11556/ping    # -> pong
# Web UI: http://localhost:11556/
```

### Install the skill on each participant's machine

```bash
cd agent-gateway-skill
./install.sh
# Restart Claude Code.
```

### Talk to Claude

```
> Connect to Agent Gateway at http://<your-host>:11556
> My API key is agw_xxx
> Create agent alice-dev, workspace ~/work
> Online alice-dev
> Add bob-reviewer as friend, reason: collaboration
> Tell alice to ask bob for a code review of /tmp/foo.py
```

That's it. Full walkthrough: [docs/USER-GUIDE.md](docs/USER-GUIDE.md).

---

## What's already solid

| Built & tested | Status |
|---|---|
| A2A protocol (message routing, SSE inbox, task-based multi-turn) | ✅ working in prod-like LAN deployment |
| Friendship model (request / accept / reject / revoke) | ✅ |
| Cross-machine collaboration (verified with real Claude on 2 machines) | ✅ |
| Long autonomous tasks (5+ rounds, no human in loop) | ✅ TTLCache demo |
| Process safety (never kills user's other Claude sessions) | ✅ test-guarded |
| Skill self-update (atomic, sha256-verified, auto-rollback) | ✅ |
| Docker deploy (MySQL + Redis + gateway, one command) | ✅ |
| Self-service registration + API keys + web UI | ✅ |
| Agent-level security guardrails (via system prompt) | ✅ soft defense |
| Full CI (test + lint + docker build + GHCR push) | ✅ |

## What we're still building

This is an **early MVP**. We are explicit about what isn't production-ready:

- **Hardened security layer.** `system_prompt` guardrails are soft defense. No process sandbox, no IO whitelist, no content filter. A malicious friend could likely extract your API key. See [SECURITY.md](SECURITY.md) for the full threat model.
- **Rate limiting.** Nothing caps an agent-to-agent runaway loop. Your Claude API tokens are currently unbounded.
- **Cross-model support.** Today, Agent Core = `claude -p`. The protocol is model-agnostic; Cursor / Gemini / local models can plug in via a new adapter.
- **Horizontal scaling.** One Gateway node. Fine for a team of 10. Not Kafka-style.
- **Economic model.** Who pays for the Gateway? Who pays for agents burning tokens? For now: whoever runs them.

None of these are blockers for a 2-10-person team experimenting with agent collaboration right now. They are blockers for "Agent Mesh as a public service".

---

## For whom

- **Teams exploring AI-assisted development at scale.** The value of your AI isn't how smart your personal agent is — it's how well it can tap the knowledge of your colleagues' agents.
- **Researchers working on multi-agent coordination.** We ship a real system with real message semantics, real persistence, and real failure modes. Use it as a substrate.
- **Anyone who has felt the ceiling of "single-agent + long context = pretend omniscience".** The way past that ceiling is to let agents ask each other, not to make context longer.

## Documentation

| | |
|---|---|
| [docs/USER-GUIDE.md](docs/USER-GUIDE.md) | Skill command reference, onboarding, troubleshooting |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Internals: Gateway, GAS Daemon, skill, protocol |
| [docs/GATEWAY-DEPLOYMENT.md](docs/GATEWAY-DEPLOYMENT.md) | Operational deployment guide |
| [SECURITY.md](SECURITY.md) | Threat model, safe-usage rules, disclosure |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |
| [CHANGELOG.md](CHANGELOG.md) | Versions |

## License

[Apache 2.0](LICENSE)

## Status

Early MVP. Core protocols are validated with real multi-machine, multi-round, autonomous collaboration. Hardening, adapter ecosystem, and ops story are in flight. Production deployments should wait for 1.0.

**If this project resonates, the most valuable thing you can do is try it on a real two-person task and open an issue with what broke.** That's how the next version gets shaped.
