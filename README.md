# claude-whisper

> Inter-instance communication that costs zero tokens and zero daemons. Works everywhere — CLI, VS Code, JetBrains, Cowork.

Lightweight Inter-Process Communication (IPC) for [Claude Code](https://claude.ai/code) instances. The filesystem is the message bus. Hooks are the event loop.

## The problem

Running multiple Claude Code instances on the same machine? They can't talk to each other. Existing solutions require daemons, databases, runtime dependencies, and burn tokens on polling. Worse — they only work in the CLI. Switch to VS Code or JetBrains and you lose inter-instance communication entirely.

## The solution

**claude-whisper** uses the filesystem as a message bus and Claude Code's native hooks as the event loop. Messages are JSON files. Delivery is atomic. Reception costs zero tokens when the inbox is empty.

**Works everywhere Claude Code runs** — CLI, VS Code, JetBrains, Desktop app. No plugin compatibility issues, no CLI-only limitations. Hooks are defined at user level, active across all surfaces.

```
Instance A                    Filesystem                    Instance B
    |                    ~/.claude-whisper/                      |
    |-- whisper-send B "hello" -->                               |
    |    writes inbox/B/msg.json                                 |
    |                                                            |
    |                         <-- user types a prompt -----------|
    |                    hook reads inbox, injects context       |
    |                                        Claude sees msg --> |
```

## Quick start

```bash
git clone https://github.com/druide67/claude-whisper.git
cd claude-whisper
bash bin/whisper-init my-project
```

That's it. The hook is installed, your peer is registered.

**Or just tell Claude:**

> Install claude-whisper for this project. Clone https://github.com/druide67/claude-whisper
> into ~/claude-whisper if not already there, then run `bash ~/claude-whisper/bin/whisper-init <my-peer-id>`
> from my project directory.

## Usage

```bash
# Send a message to another instance
bash ~/claude-whisper/bin/whisper-send <peer-id> "Check your imports, I changed the auth API"

# Broadcast to all registered peers
bash ~/claude-whisper/bin/whisper-broadcast "Deploying v2.0 in 5 minutes"

# Messages arrive automatically at the recipient's next prompt
# No action needed on the receiving side
```

### Message format

Messages support full markdown. Send rich, structured updates:

```bash
bash ~/claude-whisper/bin/whisper-send backend "## Auth refactor done

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

## How it works

| Component | Role |
|-----------|------|
| `bin/whisper-init` | Creates `~/.claude-whisper/`, registers peer, installs hook |
| `bin/whisper-send` | Writes a JSON message to the recipient's inbox (atomic) |
| `bin/whisper-broadcast` | Sends to all registered peers |
| `hooks/check-inbox.sh` | `UserPromptSubmit` hook — reads inbox, injects into Claude's context |

The hook runs at every prompt. When the inbox is empty, it exits silently in <5ms — zero tokens consumed, zero overhead.

## Comparison

| | claude-whisper | claude-peers-mcp | claude-ipc-mcp |
|---|---|---|---|
| **Lines of code** | ~200 | ~2,000 | ~2,200 |
| **Daemon** | None | HTTP broker | TCP broker |
| **Database** | Filesystem | SQLite | SQLite |
| **Runtime** | bash + jq | Bun | Python 3.12 |
| **Tokens at rest** | 0 | ~500-800/poll | ~50-200/poll |
| **Network surface** | None | localhost:7899 | localhost:9876 |
| **Setup time** | < 1 min | 5-10 min | 10-15 min |
| **IDE support** | CLI, VS Code, JetBrains, Desktop, Cowork | CLI only | CLI only |

## Requirements

- **macOS** or **Linux** (WSL on Windows)
- **bash** (v3+)
- **jq** (`brew install jq` / `apt install jq`)
- **Claude Code** v2+ (CLI, VS Code, JetBrains, Desktop, Cowork)

## Install prompt

Copy-paste this into any Claude Code instance to install whisper:

```
Clone https://github.com/druide67/claude-whisper into ~/claude-whisper if not already there.
Then run: bash ~/claude-whisper/bin/whisper-init <PEER-ID>
Replace <PEER-ID> with a short name for this project (alphanumeric + dash, e.g. "my-app" or "backend").
Run the command from this project's root directory.
```

## Multi-instance setup

Register each project with a unique peer-id:

```bash
cd ~/projects/frontend  && bash ~/claude-whisper/bin/whisper-init frontend
cd ~/projects/backend   && bash ~/claude-whisper/bin/whisper-init backend
cd ~/projects/mobile     && bash ~/claude-whisper/bin/whisper-init mobile
```

The hook is user-level — installed once, active everywhere. Each project gets its own identity via a `.whisper-peer` file in the project root (add it to your `.gitignore`).

## Security

- **No network surface** — everything stays on the local filesystem
- **Unix permissions** — `~/.claude-whisper/` is `0700`, messages are `0600`
- **Atomic writes** — messages written to `.tmp` then moved (no partial reads)
- **Input validation** — peer-ids restricted to `[a-zA-Z0-9-]`, path traversal blocked
- **No secrets** — messages are plain text files, don't send credentials

## License

Apache 2.0
