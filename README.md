# claude-whisper

[![Tests](https://github.com/druide67/claude-whisper/actions/workflows/tests.yml/badge.svg)](https://github.com/druide67/claude-whisper/actions/workflows/tests.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](#requirements)
[![macOS](https://img.shields.io/badge/macOS-000000?logo=apple&logoColor=white)](#requirements)
[![Linux](https://img.shields.io/badge/Linux-FCC624?logo=linux&logoColor=black)](#requirements)
[![zero daemon](https://img.shields.io/badge/zero%20daemon-purple)](#)

> Inter-instance communication that costs zero tokens and zero daemons. Works everywhere — CLI, VS Code, JetBrains, Desktop.

Your [Claude Code](https://claude.ai/code) instances can now talk to each other. One static binary — no server, no database, no runtime dependencies.

<p align="center">
  <img src="assets/demo.gif" alt="demo" width="600">
</p>

## The problem

Existing solutions for multi-instance communication only work in the CLI — switch to VS Code or JetBrains and you're out of luck. They also require daemons, databases, runtime dependencies, and burn tokens on polling.

## The solution

**claude-whisper** uses the filesystem as a message bus and Claude Code's native hooks as the event loop. Messages are JSON files. Delivery is atomic. Reception costs zero tokens when the inbox is empty.

**Works everywhere Claude Code runs** — CLI, VS Code, JetBrains, Desktop. No plugin compatibility issues, no CLI-only limitations. Hooks are defined at user level, active across all surfaces.

## Getting started

### 1. Install the binary (once per machine)

```bash
go install github.com/druide67/claude-whisper/cmd/whisper@latest
```

<details>
<summary>Or build from source</summary>

```bash
git clone https://github.com/druide67/claude-whisper.git
cd claude-whisper
go build -o ~/.local/bin/whisper ./cmd/whisper
```
</details>

### 2. Tell Claude to install

Open your project in Claude Code (CLI, VS Code, or JetBrains) and say:

> Install whisper for this project with peer-id "my-app". Run `whisper init my-app`.

Claude executes the command — it registers the peer and installs the hook. **That's it — one step per project.**

Repeat for each project with a unique peer-id (e.g. `backend`, `mobile`, `api`).

## Commands

| Command | Description |
|---------|-------------|
| `whisper init <peer-id>` | Register a project and install the hook |
| `whisper send <peer-id> "message"` | Send a message to a peer |
| `whisper send -t <thread> <peer-id> "message"` | Send with a thread tag (e.g. `auth-refactor`) |
| `whisper send -s <session-title> <peer-id> "message"` | Target ONE conversation of the peer (or `-s '*'` for all) |
| `whisper send -r <msg-id> <peer-id> "message"` | Reply to a message (tracks the reply chain, detects loops) |
| `whisper broadcast "message"` | Send to all registered peers |
| `whisper list` | List peers with inbox status |
| `whisper clean [days]` | Remove archived messages older than N days (default: 7) |
| `whisper doctor` | Diagnose the whole bus: orphan inboxes, stale peers, unresolved warnings |
| `whisper rehome <wrong> <right>` | Move messages sent to a misspelled peer-id |

Messages are received automatically — the hook injects them into Claude's context at the next prompt. Empty inbox = silent, zero tokens.

## Message format

Messages support full markdown. Send rich, structured updates:

```bash
whisper send backend "## Auth refactor done

- New \`AuthProvider\` with OAuth2 support
- **Breaking**: \`getUser()\` now returns \`Promise<User | null>\`
- Run \`npm update @app/auth\` before merging"
```

The recipient sees:

```
━━━ 📨 whisper — 1 message(s) ━━━
> **frontend** (14:32): ## Auth refactor done
>
> - New `AuthProvider` with OAuth2 support
> - **Breaking**: `getUser()` now returns `Promise<User | null>`
> - Run `npm update @app/auth` before merging
━━━
```

Thread tags appear in brackets:

```
━━━ 📨 whisper — 1 message(s) ━━━
> **frontend** (14:32) [auth-refactor]: Check your imports
━━━
```

## How it works

1. **Send** — `whisper send` writes a JSON file to `~/.claude-whisper/inbox/<peer>/`
2. **Receive** — a `UserPromptSubmit` hook (`whisper check-inbox`) checks the inbox at every prompt
3. **Empty inbox** — the hook exits silently in milliseconds — zero tokens, zero overhead

```
💻 Instance A                        💻 Instance B
     │                                    │
     │  whisper send B "hello"            │
     └──── 📄 ──→ ~/.claude-whisper/ ─────┘
                    inbox/B/msg.json
                         │
              user types a prompt
                         │
                    📨 hook reads inbox
                    ✉️  message shown to Claude
```

### Reliability

Nothing fails silently. Anything that could lose or stall a message — an inbox nobody reads, a message that can't be archived, a runaway reply loop, a corrupt state file — writes a **sentinel** under `state/warnings/` and surfaces in `whisper doctor`. Sends to unregistered peers are refused (typos don't become black holes), duplicates within a short window are deduplicated, and every write is atomic.

## Advanced

### Multiple sessions on one project — target the right conversation

Running several Claude Code conversations in parallel on the same project? Rename each session in the UI (the pencil next to the session name) and its title becomes an **address**:

```bash
whisper send -s "SEO" website "the sitemap question is for you"
whisper send -s '*' website "FYI everyone: deploy at 5pm"
```

Only the session titled `SEO` sees the first message — the others aren't even shown it. `-s '*'` goes to every live session (archived once all have seen it). Without `-s`, nothing changes: first session to prompt takes the message.

Delivery is exactly-once (atomic claim before rendering), and nothing is ever lost: a message targeting a title nobody carries waits, shows up in `whisper doctor`, and after 8 hours (`WHISPER_ROUTE_TIMEOUT`) is loudly handed to the next session as metadata. `whisper list --sessions` shows which sessions are addressable — a session that was never renamed is anonymous and can't be targeted.

### Cross-machine peers (SSH transport)

A peer on another machine — even a non-Claude agent — can join the bus. Register it with `whisper init <peer-id> --transport ssh-spool`, and lock an SSH key to the spool with a forced-command in `authorized_keys`:

```
restrict,command="/path/to/whisper spool-server <peer-id>" ssh-ed25519 AAAA…
```

The remote side polls with three whitelisted verbs (`fetch` / `deposit` / `confirm`) — the key can spool messages for that one peer and nothing else: no shell, no other commands, identity enforced (`from` is forced to the key's peer-id), recipients validated, payloads bounded, backlog capped.

## Comparison

| | claude-whisper | claude-peers-mcp | claude-ipc-mcp |
|---|---|---|---|
| **Daemon** | None | HTTP broker | TCP broker |
| **Database** | Filesystem | SQLite | SQLite |
| **Runtime** | single static binary | Bun | Python 3.12 |
| **Tokens at rest** | 0 | ~500-800/poll | ~50-200/poll |
| **Network surface** | none (opt-in SSH transport) | localhost:7899 | localhost:9876 |
| **Setup time** | < 1 min | 5-10 min | 10-15 min |
| **IDE support** | CLI, VS Code, JetBrains, Desktop | CLI only | CLI only |

*Competitor figures are approximate, based on public repositories.*

## Requirements

- **macOS** or **Linux** (WSL on Windows)
- **Go 1.22+** to build (none at runtime — the binary is static)
- **Claude Code** v2+ (CLI, VS Code, JetBrains, Desktop)

## Security

- **No network surface by default** — everything stays on the local filesystem. The optional SSH transport is the single network-facing entry point, locked down by an SSH forced-command (whitelisted verbs, never evaluated).
- **Unix permissions** — `~/.claude-whisper/` is `0700`, messages are `0600`
- **Atomic writes** — unique temp file + rename (no partial reads), cross-process mutations under `flock`
- **Input validation** — peer-ids restricted to `[a-zA-Z0-9-]`, path traversal blocked, single validation source
- **No secrets** — messages are plain text files, don't send credentials

## Limitations

- **Cowork**: can send messages but cannot receive automatically (sandbox limitation).
- **Not real-time**: messages are delivered at the recipient's next prompt, not instantly.

## Contributing

Issues and PRs welcome. Run `go test ./...` before submitting. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

If claude-whisper is useful to you, a star helps others discover it.

## License

Apache 2.0
