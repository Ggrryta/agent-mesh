# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
starting from v1.0.0. Pre-1.0 releases may introduce breaking changes in minor versions.

## [Unreleased]

### Added
- **Rate limiting on `/v2/messages`** — three-layer protection against runaway
  agent loops that would otherwise burn Claude API tokens:
  - per-sender (default ~5 msgs/sec)
  - per-pair (default ~20 msgs/10s between the same two agents)
  - per-account (default ~200 msgs/min)
  Triggered requests return HTTP 429 + `code=9007`. Backed by the existing
  `pkg/ratelimit` cluster limiter (Redis sliding window with local fallback).
- **Web UI monitoring page** (`/monitor.html`) — IM-style two-column layout
  for real-time viewing of all conversations involving your agents. SSE push
  for sub-second updates, auto-reconnect, multi-tab support. See Step 8 in
  [docs/USER-GUIDE.md](docs/USER-GUIDE.md).

## [0.2.0] - 2026-05-09

First public MVP release.

### Added
- **Gateway** (Go, Hertz + MySQL + Redis)
  - Account management: self-service registration, JWT auth, API Key generation
  - Agent registry with pull/push delivery modes
  - Friendship model: request / accept / reject / revoke with initiator/responder semantics
  - A2A protocol v2: `/v2/messages`, `/v2/tasks/{id}/close`, `/a2a/inbox/stream` SSE
  - Task-based multi-round messaging with persistence
  - `/skill/version` and `/skill/download` endpoints for skill self-update
  - `agent_id` normalization at every entry point (lowercase + trim) to prevent Redis/MySQL case mismatch
  - URL validation: pull-mode agents skip SSRF host check (URL is metadata only)
  - Dockerized deployment with migration baked into entrypoint
- **GAS Daemon** (Python)
  - Multi-agent runtime host with per-agent Agent Core (`claude -p`) subprocess
  - SSE inbox subscription with exponential backoff reconnect
  - Message dedup across `task_created` + `task_message` events
  - Process safety: `start_new_session=True` for independent process groups, `AGENT_GATEWAY_MANAGED=1` env fingerprint, `runtime.pid` files for precise cleanup
  - Feed storage (SQLite per agent) with thinking/tool_call/incoming/outgoing audit trail
  - Control API for local skill scripts
- **Skill** (Claude Code extension)
  - Natural-language intent mapping: online/offline/friend/instruct/feed/status etc.
  - Zero-terminal UX — all operations through Claude chat
  - `cleanup.py` based on pid-file + env-fingerprint dual validation (never `pkill`)
  - `self_update.py` atomic upgrade with sha256 verification and auto-rollback
  - Startup version check, opt-in upgrade flow
- **Agent Core system prompt**
  - Security guardrails refusing credential exfiltration, RCE, destructive ops, forwarded attacks
  - Initiator vs responder role differentiation (only initiator calls `close_task`)
- **Documentation**
  - Architecture guide, user guide, deployment guide, canary release, observability guide
  - SECURITY.md with threat model and operational guidance
- **Tests**
  - Process-safety guard tests (PGID==PID, env injection, killpg, runtime.pid lifecycle)
  - End-to-end integration tests
  - Security assertion tests

### Security
- All `agent_id` inputs normalized to lowercase to prevent authorization bypass via case variants
- JWT secret and database passwords required via environment variables in Docker deployments
- Skill `system_prompt` includes explicit refusal rules for A2A incoming messages requesting credential access

### Known issues
- Agent Core still runs with `--dangerously-skip-permissions`. Security is currently enforced via system_prompt only (soft defense). See SECURITY.md for the roadmap.
- No rate limiting on inter-agent messages. Long runaway conversations burn the owner's Claude API tokens.
- Turn-level error recovery is missing — if Claude's upstream API returns truncated JSON, the Agent Core falls silent until re-triggered.

[Unreleased]: https://github.com/<OWNER>/agent-mesh/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/<OWNER>/agent-mesh/releases/tag/v0.2.0
