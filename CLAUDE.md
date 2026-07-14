# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# claude-whisper

## Identity
OSS project: Inter-instance IPC for Claude Code.
Philosophy: "The filesystem IS the message bus. Hooks ARE the event loop."

## Principles
- Zero daemon, zero tokens at rest, zero runtime dependencies (one static Go binary)
- Works on CLI, VS Code, JetBrains, Desktop (hooks in `~/.claude/settings.json` — user scope)
- Cowork: send only (sandbox limitation — hook can't access host filesystem)
- Fail-loud: anything that could lose or stall a message writes a sentinel under `state/warnings/` instead of failing silently
- Security by design: Unix permissions, no network surface by default; the optional SSH transport (`whisper spool-server`) is the single, opt-in network-facing entry point, locked down by an SSH forced-command

## Dev commands

```bash
go build ./...            # build everything
go vet ./...              # static checks
go test ./...             # full test suite
go test ./internal/cmd -run TestSend -v          # one test
go build -o ~/.local/bin/whisper ./cmd/whisper   # install locally

# CLI usage (one binary, git-style subcommands)
whisper init <peer-id> [project-dir]
whisper send [-t thread] [-f from] [-r reply-to] [-s session|'*'] [-F] <peer> "message"
whisper broadcast [-t thread] [-f from] "message"
whisper list [--sessions]
whisper clean [days]
whisper doctor [--fix] [--yes] [--list-orphans]
whisper rehome <wrong-peer> <correct-peer> [--yes]
```

## Architecture

### Communication flow

```
Instance A (sender)          Filesystem                    Instance B (receiver)
       |                    ~/.claude-whisper/                      |
       |-- whisper send B "hello" -->                               |
       |    writes inbox/B/msg-<ts>.json                            |
       |                                                            |
       |                         <--[user types a prompt]-----------|
       |                    hook `whisper check-inbox` runs         |
       |                    injects messages via plain stdout       |
       |                                          [LLM sees msgs]-->|
```

The `UserPromptSubmit` hook is the event loop — runs at every prompt, 0 tokens when the inbox is empty.

### One binary, three entry points

`cmd/whisper/main.go` dispatches to one function per subcommand:
- **CLI**: `whisper send|broadcast|list|clean|init|rehome|doctor`
- **Hook**: `whisper check-inbox` — reads the Claude Code event JSON on stdin, renders pending messages on stdout (plain text, exit 0), archives what it delivered. Consuming (archiving) is gated by an event ALLOWLIST (`UserPromptSubmit`, `SessionStart`): unknown/future events render without consuming, so a message is never archived toward a surface that can't display it.
- **SSH transport**: `whisper spool-server <peer-id>` — forced-command target for remote peers. Spool-only: verbs `fetch`/`deposit`/`confirm` from `$SSH_ORIGINAL_COMMAND` are whitelisted and never evaluated; the peer-id is the trust anchor frozen in `authorized_keys`; `deposit` forces `from == peer-id`, validates the recipient against `peers.json` (fail-closed if absent), bounds stdin, rejects anything `check-inbox` couldn't later parse, never overwrites, and caps the per-recipient backlog.

### Internal packages

- `internal/peerid` — THE validation regexes (peer-id, session-id, msg-id). Single source; never inline a copy.
- `internal/msg` — `Message` type + stable JSON (de)serialization. Defaults applied on parse (`priority:"normal"`, `ttl:3600`).
- `internal/store` — paths, `AtomicWrite` (unique tmp + rename), `Lock` (flock), `UpdatePeers` (locked read-modify-write), config. All cross-process mutation goes through this package. `peers.json` entries are `map[string]any` so unknown/future fields survive upserts.
- `internal/warn` — THE atomic sentinel writer/clearer (`state/warnings/<kind>-<key>.warn`). Single source.
- `internal/multi` — multi-session state (see below).
- `internal/cmd` — one function per subcommand, each returning an exit code.

### Session-title routing (multi-session)

Several Claude Code conversations can share one project/peer; a message can target ONE of them by its user-set session title (the rename pencil in the UI): `whisper send -s "Some-Title" <peer> …`. Rules, in order of trust:

- **The message's own `session` field alone decides its fate.** A session that cannot prove its identity (no/invalid `session_id`, unreadable state) never consumes a targeted message — mis-delivery is structurally impossible, worst case is a loud wait.
- Identity = the hook event's `session_title` (undocumented field; absent = anonymous), **confirmed against the last `custom-title` record in the transcript tail before any claim** — the event value can lag one beat behind a UI rename.
- **Exactly-once via owned claims**: delivery renames the file into `run/claims/<session-id>/` before anything is emitted. Claims of dead sessions (heartbeat past `WHISPER_SESSION_GRACE`, default 36h) are re-inboxed; a session re-delivers its own leftovers on its next run (re-delivery over loss, always).
- `-s '*'` broadcasts: every live session renders it, archived once all have seen it (seen-sets in `state/multi/<peer>.json`, mutated under flock).
- No `-s` = classic consume-once, byte-identical to the historical behavior.
- **Escalation**: a targeted message unclaimed past `WHISPER_ROUTE_TIMEOUT` (default 8h) is handed to the next consuming session as *metadata only* (from/target/archive path — never the content) with an `unrouted` sentinel; an `unrouted-pending` sentinel appears as soon as no live session carries the title. `'*'` never escalates before the grace.
- Title grammar (enforced at `send` AND `spool deposit` — the field arrives from the network): NFC-normalized, trimmed, ≤ 64 runes, no control characters, `*` reserved.
- Renames re-identify: a message sent to your OLD title before you renamed still finds you (bounded by the rename observation instant — never by the registry, which is advisory only, e.g. for the send-time "nobody carries this title" warning and `whisper list --sessions`).

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
  "hop_count": 0,
  "thread": "auth-refactor"
}
```

`thread` (via `-t`) and `in_reply_to` (via `-r`) are optional. `hop_count` follows the explicit reply chain and powers loop detection (soft warning past `WHISPER_HOP_MAX`, hard drop + sentinel past `WHISPER_HOP_HARD`).

### The filesystem contract

The on-disk layout is a public interface — external tools (menu-bar apps, editor extensions) read it directly. Treat any change to these as a breaking change:
- `~/.claude-whisper/peers.json` — registry (`registered`, `last_seen`, `cwd`, optional `transport`)
- `inbox/<peer>/msg-*.json`, `archive/`
- `state/config.json` — `{autoGlobal, modePerPeer}` (delivery modes for UI integrations)
- `state/recent-sends.json` — anti-duplicate ledger
- `state/warnings/*.warn` — fail-loud sentinels
- `.whisper-peer` — peer-id file in each project root (gitignored)

### Hook output

Plain text on stdout (exit code 0). Do NOT use JSON `hookSpecificOutput` — it causes "UserPromptSubmit hook error" in practice despite being documented.

## Conventions
- Atomic writes everywhere: unique tmp file + `rename` (see `store.AtomicWrite`)
- Cross-process read-modify-write only under `store.Lock` (flock on `<target>.lock`)
- `~/.claude-whisper/` is `0700`, files are `0600`
- Validation only via `internal/peerid` (peer-id: `^[a-zA-Z0-9][a-zA-Z0-9-]*$`)
- Sentinels only via `internal/warn`; a resolved condition must clear its sentinel
- Hook stays fast: no network calls, no subprocess spawning in `check-inbox`
- Tunables are env vars with safe defaults (`WHISPER_DIR`, `WHISPER_DUP_WINDOW`, `WHISPER_INLINE_MAX`, `WHISPER_OUTPUT_MAX`, `WHISPER_HOP_MAX`, `WHISPER_HOP_HARD`, `WHISPER_SESSION_GRACE`, `WHISPER_ROUTE_TIMEOUT`, `WHISPER_MAX_CONTENT_BYTES`, `WHISPER_MAX_PAYLOAD_BYTES`, `WHISPER_SPOOL_MAX_PENDING`, `WHISPER_STALE_DAYS`); a non-numeric value clamps to the default and never disables a safety mechanism

## Known technical constraints
- **Bug #10225**: hooks in plugins don't execute → define in `~/.claude/settings.json`
- **hookSpecificOutput broken**: JSON format causes hook errors → use plain stdout
- **Hook format in settings.json**: `{"matcher": "", "hooks": [{"type": "command", "command": "..."}]}`
- **Hook latency**: keep `whisper check-inbox` fast (< 200 ms; empty inbox is ~ms)

## Structure
- `cmd/whisper/` — main dispatch
- `internal/` — packages above, tests alongside as `*_test.go`

## Working rules
- On complex problems: PROPOSE a solution and WAIT for approval before implementing
- On simple fixes (typos, obvious bugs): fix directly
