# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# claude-whisper

## Identity
OSS project: Inter-instance IPC for Claude Code.
Philosophy: "The filesystem IS the message bus. Hooks ARE the event loop."

## Principles
- Zero daemon, zero tokens at rest, zero dependencies (bash + jq)
- Works on CLI, VS Code, JetBrains, Desktop (hooks in `~/.claude/settings.json` — user scope)
- Cowork: send only (sandbox limitation — hook can't access host filesystem)
- Security by design: Unix permissions, no network surface
- ~300 LOC total

## Dev commands

```bash
# Run all tests
bats tests/

# Run a specific test
bats tests/check-inbox.bats

# Syntax check
bash -n hooks/check-inbox.sh
bash -n bin/whisper-send

# Test commands (symlinks in ~/.local/bin/)
whisper-list
whisper-send <peer-id> "message"
whisper-send -t <thread> <peer-id> "message"
whisper-send -f <from> <peer-id> "message"
whisper-broadcast "message"
whisper-clean [days]
```

## Architecture

### Communication flow

```
Instance A (sender)          Filesystem                    Instance B (receiver)
       |                    ~/.claude-whisper/                      |
       |-- whisper-send B "hello" -->                               |
       |    writes inbox/B/msg-<ts>.json                            |
       |                                                            |
       |                         <--[user types a prompt]-----------|
       |                    hook check-inbox.sh runs                |
       |                    injects messages via plain stdout       |
       |                                          [LLM sees msgs]-->|
```

The `UserPromptSubmit` hook is the event loop — runs at every prompt, 0 tokens when inbox is empty.

### Components

**Reception** (passive, filesystem → LLM):
- `hooks/check-inbox.sh` — `UserPromptSubmit` hook: reads `.whisper-peer` from CWD, scans `~/.claude-whisper/inbox/<peer-id>/msg-*.json`, outputs plain text to stdout (exit 0), archives read messages, silent exit if inbox empty

**Sending** (active, LLM → filesystem):
- `bin/whisper-send` — sends a message to a peer's inbox (atomic write via `.tmp` + `mv`). Flags: `-t thread`, `-f from`
- `bin/whisper-broadcast` — sends to all peers except self. Passes through `-t` and `-f` flags
- `bin/whisper-init` — setup: creates `~/.claude-whisper/`, installs hook in `settings.json`, creates symlinks in `~/.local/bin/`, writes `.whisper-peer` in project dir

**Utilities**:
- `bin/whisper-list` — lists peers with inbox count
- `bin/whisper-clean` — removes archived messages older than N days (default: 7)

**Registry**:
- `~/.claude-whisper/peers.json` — registered peers with `last_seen` and `cwd`
- `.whisper-peer` — peer-id file in each project's root directory (gitignored)

### Message format

`inbox/<peer-id>/msg-<timestamp>-<rand>.json`:
```json
{
  "id": "msg-1712150400-a3f2",
  "from": "frontend",
  "to": "backend",
  "timestamp": "2026-04-03T14:00:00Z",
  "content": "...",
  "priority": "normal",
  "ttl": 3600,
  "thread": "auth-refactor"
}
```

The `thread` field is optional (set via `-t` flag).

### Hook output

Plain text on stdout (exit code 0). Do NOT use JSON `hookSpecificOutput` — it causes "UserPromptSubmit hook error" in practice despite being documented.

## Conventions
- Messages JSON in `~/.claude-whisper/inbox/<peer-id>/`
- Atomic naming: write to `.tmp` then `mv`
- `~/.claude-whisper/` is `0700`, messages are `0600`
- Peer-id validation: alphanumeric + dash only (`^[a-zA-Z0-9-]+$`)
- Output truncated at 9000 chars to stay under 10k limit

## Known technical constraints
- **Bug #10225**: hooks in plugins don't execute → define in `~/.claude/settings.json`
- **hookSpecificOutput broken**: JSON format causes hook errors → use plain stdout
- **Hook format in settings.json**: `{"matcher": "", "hooks": [{"type": "command", "command": "..."}]}`
- **Hook latency**: stay < 200ms (shell only, no network requests)

## Structure
- `bin/` — CLI commands (`whisper-init`, `whisper-send`, `whisper-broadcast`, `whisper-list`, `whisper-clean`)
- `hooks/` — Claude Code hooks (`check-inbox.sh`)
- `tests/` — bats-core tests (32 tests)

## Working rules
- On complex problems: PROPOSE a solution and WAIT for approval before implementing
- On simple fixes (typos, obvious bugs): fix directly
