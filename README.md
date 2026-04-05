# claude-whisper

> Inter-instance communication that costs zero tokens and zero daemons. Works everywhere — CLI, VS Code, JetBrains, Desktop.

Lightweight Inter-Process Communication (IPC) for [Claude Code](https://claude.ai/code) instances. The filesystem is the message bus. Hooks are the event loop.

## The problem

Running multiple Claude Code instances on the same machine? They can't talk to each other. Existing solutions require daemons, databases, runtime dependencies, and burn tokens on polling. Worse — they only work in the CLI. Switch to VS Code or JetBrains and you lose inter-instance communication entirely.

## The solution

**claude-whisper** uses the filesystem as a message bus and Claude Code's native hooks as the event loop. Messages are JSON files. Delivery is atomic. Reception costs zero tokens when the inbox is empty.

**Works everywhere Claude Code runs** — CLI, VS Code, JetBrains, Desktop app, Cowork. No plugin compatibility issues, no CLI-only limitations. Hooks are defined at user level, active across all surfaces.

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

## Getting started

### 1. Clone (once per machine)

```bash
git clone https://github.com/druide67/claude-whisper.git ~/claude-whisper
```

### 2. Tell Claude to install

Open your project in Claude Code (CLI, VS Code, or JetBrains) and say:

> Install whisper for this project. Run `bash ~/claude-whisper/bin/whisper-init my-app` (replace `my-app` with a short name for this project).

Claude will execute the command, see the available commands in the output, and save them to its memory. **That's it — one step per project.**

Repeat for each project with a unique peer-id:

> Install whisper with peer-id "backend"

> Install whisper with peer-id "mobile"

After the first init, `whisper-init` is in the PATH — Claude can just run `whisper-init <peer-id>`.

<details>
<summary>Manual setup (without Claude)</summary>

```bash
cd ~/projects/my-app && bash ~/claude-whisper/bin/whisper-init my-app
```

Then copy-paste the onboarding prompt (printed at the end) into your Claude Code session so it learns the commands.
</details>

## Commands

| Command | Description |
|---------|-------------|
| `whisper-init <peer-id>` | Register a project and install the hook |
| `whisper-send <peer-id> "message"` | Send a message to a peer |
| `whisper-send -t <thread> <peer-id> "message"` | Send with a thread tag (e.g. `auth-refactor`) |
| `whisper-broadcast "message"` | Send to all registered peers |
| `whisper-list` | List peers with inbox status |
| `whisper-clean [days]` | Remove archived messages older than N days (default: 7) |

Messages are received automatically — the hook injects them into Claude's context at the next prompt. Empty inbox = silent, zero tokens.

## Message format

Messages support full markdown. Send rich, structured updates:

```bash
whisper-send backend "## Auth refactor done

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

## Comparison

| | claude-whisper | claude-peers-mcp | claude-ipc-mcp |
|---|---|---|---|
| **Lines of code** | ~300 | ~2,000 | ~2,200 |
| **Daemon** | None | HTTP broker | TCP broker |
| **Database** | Filesystem | SQLite | SQLite |
| **Runtime** | bash + jq | Bun | Python 3.12 |
| **Tokens at rest** | 0 | ~500-800/poll | ~50-200/poll |
| **Network surface** | None | localhost:7899 | localhost:9876 |
| **Setup time** | < 1 min | 5-10 min | 10-15 min |
| **IDE support** | CLI, VS Code, JetBrains, Desktop | CLI only | CLI only |

## Requirements

- **macOS** or **Linux** (WSL on Windows)
- **bash** (v3+)
- **jq** (`brew install jq` / `apt install jq`)
- **Claude Code** v2+ (CLI, VS Code, JetBrains, Desktop)

## Security

- **No network surface** — everything stays on the local filesystem
- **Unix permissions** — `~/.claude-whisper/` is `0700`, messages are `0600`
- **Atomic writes** — messages written to `.tmp` then moved (no partial reads)
- **Input validation** — peer-ids restricted to `[a-zA-Z0-9-]`, path traversal blocked
- **No secrets** — messages are plain text files, don't send credentials

## Limitations

- **Cowork (sandbox)**: Cowork sessions run in a Linux sandbox. They can **send** messages (via Desktop Commander executing on the host), but cannot **receive** automatically (the hook runs inside the sandbox where `~/.claude-whisper/` doesn't exist). Use `whisper-send --from <peer-id>` from Cowork.
- **Single machine**: whisper uses the local filesystem — no cross-machine messaging.
- **Not real-time**: messages are delivered at the recipient's next prompt, not instantly.

## License

Apache 2.0
