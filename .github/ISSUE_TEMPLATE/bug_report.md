---
name: Bug report
about: Report a reproducible problem
title: '[Bug] '
labels: bug
---

## Summary
A one-sentence description of what's broken.

## How to reproduce
1. ...
2. ...
3. ...

## Expected behavior
What you expected to happen.

## Actual behavior
What actually happened. Include error messages verbatim.

## Environment
- OS: [e.g. macOS 15.2, Ubuntu 24.04]
- Go version: [`go version`]
- Python version: [`python3 --version`]
- Gateway version: [check `curl <gateway>/skill/version`]
- Skill version: [`cat ~/.claude/skills/agent-gateway/VERSION`]
- Deployment: [docker compose / standalone go binary / dev]

## Logs
Relevant excerpts from:
- Gateway: `docker logs agent-mesh-gateway`
- Daemon: `~/.agent-gateway/daemon.log`
- Feed (if agent behavior looks wrong): `python scripts/agent_feed.py <agent> --tail 50`

⚠️ **Redact any API keys (`agw_...`) before pasting.**
