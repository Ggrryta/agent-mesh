## Summary
<!-- 1-2 sentences describing what this PR does and why. -->

## Changes
<!-- Bullet list of the concrete code changes. -->

- 

## Testing
<!-- What did you do to verify this works? Add tests if the change is non-trivial. -->

- [ ] Unit tests pass: `go test ./...`
- [ ] Integration tests pass (if applicable): `go test -tags=integration ./test/integration/...`
- [ ] Skill tests pass (if applicable): `cd agent-gateway-skill && pytest`
- [ ] Manually verified on local deployment

## Checklist
- [ ] Code follows the existing style (`gofmt`, `ruff`)
- [ ] Commit messages follow Conventional Commits (`feat:`, `fix:`, etc.)
- [ ] Updated CHANGELOG.md under `[Unreleased]` if user-visible
- [ ] Updated docs (`README.md`, `docs/*.md`, or relevant skill docs) if behavior changed
- [ ] No new security risks introduced (see SECURITY.md)
- [ ] No TODO/FIXME/placeholder left in the changed code
