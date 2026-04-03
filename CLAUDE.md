# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# claude-whisper

## Identité
Projet OSS : IPC inter-instances Claude Code.
Philosophie : "Le filesystem EST le message bus. Les hooks SONT l'event loop."

## Principes
- Zero daemon, zero tokens au repos, zero dépendance (bash + jq)
- Compatible CLI + Cowork (hooks dans `~/.claude/settings.json` — scope user)
- Sécurité par design : permissions Unix, pas de surface réseau
- Target : < 200 LOC total

## Stack
- Bash (POSIX-compatible quand possible)
- jq pour le parsing JSON
- Hooks Claude Code (UserPromptSubmit via `hookSpecificOutput.additionalContext`)
- MCP server optionnel (bash stdio ou Node minimal, ~80 LOC)

## Commandes de développement

```bash
# Initialiser un pair (crée ~/.claude-whisper/, configure le hook)
bash bin/whisper-init <peer-id>

# Envoyer un message à un pair
bash bin/whisper-send <peer-id> "message"

# Lancer les tests (bats-core requis : brew install bats-core)
bats tests/

# Lancer un test précis
bats tests/check-inbox.bats

# Vérifier la syntaxe bash
bash -n hooks/check-inbox.sh
bash -n bin/whisper-send

# Vérifier que jq est disponible
jq --version
```

## Architecture

### Flux de communication

```
Instance A (sender)          Filesystem                    Instance B (receiver)
       |                    ~/.claude-whisper/                      |
       |--[MCP: whisper_send]-->                                    |
       |    écrit inbox/B/msg-<ts>.json                             |
       |                                                            |
       |                         <--[user tape un prompt]-----------|
       |                    hook check-inbox.sh s'exécute           |
       |                    injecte messages via additionalContext   |
       |                                          [LLM voit msgs]-->|
```

Le hook `UserPromptSubmit` est l'event loop — il tourne à chaque prompt, 0 token quand inbox vide.

### Composants et leur interaction

**Réception** (passive, filesystem → LLM) :
- `hooks/check-inbox.sh` — hook `UserPromptSubmit` : lit `~/.claude-whisper/inbox/<peer-id>/msg-*.json`, injecte via `hookSpecificOutput.additionalContext` (JSON), archive les messages lus, sort silencieusement si inbox vide
- `hooks/register-peer.sh` — hook `SessionStart` (optionnel) : met à jour `peers.json` avec `last_seen`

**Emission** (active, LLM → filesystem) :
- `bin/whisper-send` — script CLI : écrit atomiquement (`msg.tmp` → `mv`) dans `inbox/<peer-id>/`
- MCP server (`lib/mcp-server.sh` ou Node minimal) — expose 4 outils : `whisper_send`, `whisper_peers`, `whisper_status`, `whisper_broadcast`
- `bin/whisper-init` — setup initial : crée `~/.claude-whisper/` (0700), `inbox/` (0700), injecte le hook dans `~/.claude/settings.json`

**Registre** :
- `~/.claude-whisper/peers.json` — pairs actifs avec `last_seen`. Un pair absent depuis >2h est stale.
- `~/.claude-whisper/.current-peer` — peer-id de l'instance courante (lu par le hook)

### Format des fichiers

**Message** (`inbox/<peer-id>/msg-<timestamp>-<rand>.json`) :
```json
{
  "id": "msg-1712150400-a3f2",
  "from": "openclaw",
  "to": "asiai",
  "timestamp": "2026-04-03T14:00:00Z",
  "content": "...",
  "priority": "normal",
  "ttl": 3600
}
```

**Hook output** (stdout du hook, format JSON requis par le bug #13912) :
```json
{ "hookSpecificOutput": { "additionalContext": "..." } }
```

## Conventions
- Messages JSON dans `~/.claude-whisper/inbox/<peer-id>/`
- Nommage atomique : écrire en `.tmp` puis `mv` (évite les race conditions)
- Répertoire `~/.claude-whisper/` en 0700, messages en 0600
- Validation des peer-id : alphanumérique + tiret uniquement (`^[a-zA-Z0-9-]+$`)

## Contraintes techniques connues
- **Bug #10225** : hooks dans plugins ne s'exécutent pas → définir dans `~/.claude/settings.json`
- **Bug #13912** : stdout brut fragile dans `UserPromptSubmit` → utiliser JSON `hookSpecificOutput`
- **Limite** : 10 000 chars sur `additionalContext` → tronquer/résumer si nécessaire
- **Latence hook** : rester < 200ms (shell only, pas de requêtes réseau)

## Structure
- `bin/` — scripts exécutables (`whisper-init`, `whisper-send`)
- `hooks/` — hooks Claude Code (`check-inbox.sh`, `register-peer.sh`)
- `lib/` — fonctions partagées (et MCP server)
- `tests/` — tests bats-core
- `docs/` — ADR, specs, architecture

## Référence
- ADR complet : `docs/ADR - Communication Inter-Instances Claude Code.md`
