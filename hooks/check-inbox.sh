#!/usr/bin/env bash
# Hook: UserPromptSubmit — check whisper inbox
# Outputs plain text to stdout (shown to Claude via exit code 0)
# Exits silently (0 tokens) when inbox is empty

# Consume stdin (Claude Code pipes the user prompt)
cat > /dev/null

WHISPER_DIR="${WHISPER_DIR:-$HOME/.claude-whisper}"

# Read peer-id from project directory
[ ! -f ".whisper-peer" ] && exit 0
PEER_ID=$(cat ".whisper-peer")

# Validate peer-id (prevent path traversal)
echo "$PEER_ID" | grep -qE '^[a-zA-Z0-9-]+$' || exit 0

INBOX="$WHISPER_DIR/inbox/$PEER_ID"
[ ! -d "$INBOX" ] && exit 0

CONTEXT=""
COUNT=0
for msg_file in "$INBOX"/msg-*.json; do
  [ ! -f "$msg_file" ] && continue
  FROM=$(jq -r '.from // "unknown"' "$msg_file" 2>/dev/null || echo "unknown")
  CONTENT=$(jq -r '.content // ""' "$msg_file" 2>/dev/null || echo "")
  TS=$(jq -r '.timestamp // ""' "$msg_file" 2>/dev/null || echo "")
  THREAD=$(jq -r '.thread // ""' "$msg_file" 2>/dev/null || echo "")
  SHORT_TS=$(echo "$TS" | sed 's/.*T\([0-9]*:[0-9]*\).*/\1/')
  THREAD_TAG=""
  [ -n "$THREAD" ] && THREAD_TAG=" [${THREAD}]"
  CONTEXT="${CONTEXT}
> **${FROM}** (${SHORT_TS})${THREAD_TAG}: ${CONTENT}"
  COUNT=$((COUNT + 1))
  mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true

  # Truncate at 9000 chars to stay under 10k limit
  if [ ${#CONTEXT} -gt 9000 ]; then
    CONTEXT="${CONTEXT:0:9000}
> _(tronqué — messages restants conservés dans l'inbox)_"
    break
  fi
done

[ "$COUNT" -eq 0 ] && exit 0

echo "━━━ 📨 whisper — ${COUNT} message(s) ━━━${CONTEXT}
━━━
INSTRUCTION: Affiche ces messages whisper à l'utilisateur AVANT de répondre à son prompt. Utilise le format markdown ci-dessus tel quel."
