# Contributing to claude-whisper

Thanks for your interest in contributing!

## Getting started

1. Fork and clone the repo
2. Install dependencies: `brew install bats-core jq` (macOS) or `apt install bats jq` (Linux)
3. Run `bash bin/whisper-init dev` from the repo root
4. Run tests: `bats tests/`

## Making changes

- Keep it simple — the project targets ~300 LOC total
- Write tests for new features (`tests/*.bats`)
- Run `bash -n` on any modified script to check syntax
- Run `bats tests/` before submitting

## Pull requests

1. Create a branch from `main`
2. Make your changes with tests
3. Ensure all 32+ tests pass
4. Open a PR with a clear description

## Code style

- Bash with `set -euo pipefail` (except hooks — no `set -e` in hooks)
- `jq` for JSON, no other dependencies
- Atomic file writes: `.tmp` then `mv`
- Validate peer-ids: `^[a-zA-Z0-9-]+$`

## Reporting bugs

Open an issue with:
- What you expected
- What happened
- Your environment (macOS/Linux, CLI/VS Code/JetBrains)

## Ideas and discussions

Open an issue tagged `enhancement`. The project is intentionally minimal — features should justify their LOC.
