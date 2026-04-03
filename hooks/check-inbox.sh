#!/usr/bin/env bash
# Hook: UserPromptSubmit — check whisper inbox
# Injects pending messages via hookSpecificOutput.additionalContext
# Exits silently (0 tokens) when inbox is empty

set -euo pipefail

WHISPER_DIR="${WHISPER_DIR:-$HOME/.claude-whisper}"
PEER_ID=$(cat "$WHISPER_DIR/.current-peer" 2>/dev/null) || exit 0
INBOX="$WHISPER_DIR/inbox/$PEER_ID"

[ ! -d "$INBOX" ] && exit 0

shopt -s nullglob
MSG_FILES=("$INBOX"/msg-*.json)
[ ${#MSG_FILES[@]} -eq 0 ] && exit 0

NOW=$(date +%s)
CONTEXT=""
COUNT=0

for msg_file in "${MSG_FILES[@]}"; do
  # Check TTL — archive expired messages silently
  TTL=$(jq -r '.ttl // 3600' "$msg_file" 2>/dev/null || echo 3600)
  RAW_TS=$(jq -r '.timestamp // ""' "$msg_file" 2>/dev/null || echo "")
  MSG_TS=$(TZ=UTC0 date -jf "%Y-%m-%dT%H:%M:%SZ" "$RAW_TS" +%s 2>/dev/null || echo "")
  if [ -n "$MSG_TS" ] && [ $((NOW - MSG_TS)) -gt "$TTL" ]; then
    mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
    continue
  fi

  FROM=$(jq -r '.from // "unknown"' "$msg_file" 2>/dev/null || echo "unknown")
  CONTENT=$(jq -r '.content // ""' "$msg_file" 2>/dev/null || echo "")
  TS=$(jq -r '.timestamp // ""' "$msg_file" 2>/dev/null || echo "")
  CONTEXT+="From: $FROM ($TS) > $CONTENT\n"
  COUNT=$((COUNT + 1))

  # Archive after reading
  mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
done

# Nothing left after TTL filtering
if [ "$COUNT" -eq 0 ]; then exit 0; fi

CONTEXT="[whisper] $COUNT message(s):\n$CONTEXT"

# Truncate to 10000 chars
CONTEXT="${CONTEXT:0:10000}"

# Output JSON for Claude Code hook system
jq -n --arg ctx "$CONTEXT" \
  '{"hookSpecificOutput": {"additionalContext": $ctx}}'
