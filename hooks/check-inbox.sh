#!/usr/bin/env bash
# Hook: UserPromptSubmit — check whisper inbox
# Outputs plain text to stdout (shown to Claude via exit code 0)
# Exits silently (0 tokens) when inbox is empty

cat > /dev/null

WHISPER_DIR="${WHISPER_DIR:-$HOME/.claude-whisper}"
[ ! -f "$WHISPER_DIR/.current-peer" ] && exit 0
PEER_ID=$(cat "$WHISPER_DIR/.current-peer")
INBOX="$WHISPER_DIR/inbox/$PEER_ID"
[ ! -d "$INBOX" ] && exit 0

MSGS=$(find "$INBOX" -name 'msg-*.json' 2>/dev/null)
[ -z "$MSGS" ] && exit 0

CONTEXT=""
COUNT=0
for msg_file in $MSGS; do
  FROM=$(jq -r '.from // "unknown"' "$msg_file" 2>/dev/null || echo "unknown")
  CONTENT=$(jq -r '.content // ""' "$msg_file" 2>/dev/null || echo "")
  TS=$(jq -r '.timestamp // ""' "$msg_file" 2>/dev/null || echo "")
  CONTEXT="${CONTEXT}
- **${FROM}** (${TS}): ${CONTENT}"
  COUNT=$((COUNT + 1))
  mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
done

[ "$COUNT" -eq 0 ] && exit 0

echo "[whisper] ${COUNT} message(s) en attente:${CONTEXT}"
