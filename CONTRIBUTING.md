# Contributing to Agent Mesh

Thanks for your interest in contributing. This project is MVP-stage, so we're still feeling out patterns — your feedback in early issues and PRs is especially valuable.

## Ways to contribute

- **Bug reports** — find something broken? Open an issue with a reproducer.
- **Feature proposals** — open an issue first to discuss. We want to avoid spending your time on a PR that doesn't match the project direction.
- **Documentation** — docs are always behind the code. Small PRs fixing typos or clarifying sections are very welcome.
- **Security issues** — **do not** open a public issue. See [SECURITY.md](SECURITY.md) for the private disclosure process.

## Local development

Requirements:
- Go 1.25+
- Python 3.10+
- Docker + Docker Compose
- `make`, `bash`

### Setup

```bash
# 1. Bring up dependencies (MySQL + Redis)
cp .env.example .env  # set passwords
docker compose up -d mysql redis

# 2. Run the gateway locally (for iteration)
cd cmd
go run main.go -config ../config/config.yaml.example

# Or build + run:
go build -o agent-gateway ./cmd/main.go
./agent-gateway -config config/config.yaml.example
```

### Running tests

```bash
# Go unit tests
go test ./...

# Go integration tests (needs MySQL + Redis running)
go test -tags=integration ./test/integration/...

# Python skill tests
cd agent-gateway-skill
pip install pytest
python -m pytest gas/tests/
```

## PR guidelines

- Branch off `main`; name branches `feat/<thing>`, `fix/<thing>`, `docs/<thing>`
- **One logical change per PR.** Small PRs merge fast; big PRs languish.
- Include tests for behavior you add or fix. Process-safety, friendship semantics, and A2A routing are particularly important to keep test-covered.
- Run `go fmt ./...` and `ruff check agent-gateway-skill/` before committing.
- Commit message format: conventional commits (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`).

## Code style

- **Go**: standard `gofmt`, avoid `interface{}` in favor of generics when reasonable, prefer small interfaces defined at the consumer.
- **Python**: PEP-8 via `ruff`, type hints where they help, no magic (prefer explicit over implicit imports).
- Comments in new code can be English or Chinese; existing comments are mostly Chinese (the original authors were a Chinese team). Don't mass-translate — distracting for review. Do translate when you're already editing the file.

## What we won't merge

- Code with TODO/FIXME/mock values sitting in production paths
- New third-party dependencies without discussion (we try to keep surface small)
- Changes that weaken the process-safety guarantees (see [SECURITY.md](SECURITY.md) — never `pkill`, always PID-file + env fingerprint)
- Changes that bypass the agent_id normalization layer (always `NormalizeAgentID`)

## Questions

Open a Discussion or an Issue. For private/security matters, see the contact in [SECURITY.md](SECURITY.md).
