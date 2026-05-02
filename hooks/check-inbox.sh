#!/usr/bin/env bash
# Hook: UserPromptSubmit / SessionStart / Notification — check whisper inbox
# Outputs plain text to stdout
#   - UserPromptSubmit/SessionStart: injected into Claude's context
#   - Notification: shown to the user in the terminal UI
# Exits silently (0 tokens) when inbox is empty
#
# Policy:
#   - Small messages (content < WHISPER_INLINE_MAX, default 3000 chars) → inlined
#   - Big messages → reference line (length + archive path + 300-char preview)
#   - Total stdout capped at WHISPER_OUTPUT_MAX (default 8000 chars)
#   - Messages that don't fit stay in inbox for the next hook cycle
#   - A message is archived ONLY after it has been rendered into the output

# Consume stdin (Claude Code pipes hook event JSON)
cat > /dev/null

WHISPER_DIR="${WHISPER_DIR:-$HOME/.claude-whisper}"
INLINE_MAX="${WHISPER_INLINE_MAX:-3000}"
OUTPUT_MAX="${WHISPER_OUTPUT_MAX:-8000}"
PREVIEW_LEN=300

# Read peer-id from project directory
[ ! -f ".whisper-peer" ] && exit 0
PEER_ID=$(cat ".whisper-peer")

# Validate peer-id (prevent path traversal)
echo "$PEER_ID" | grep -qE '^[a-zA-Z0-9-]+$' || exit 0

INBOX="$WHISPER_DIR/inbox/$PEER_ID"
[ ! -d "$INBOX" ] && exit 0

# Collect inbox files (null if none)
shopt -s nullglob
MSG_FILES=("$INBOX"/msg-*.json)
shopt -u nullglob
TOTAL_MSGS=${#MSG_FILES[@]}
[ "$TOTAL_MSGS" -eq 0 ] && exit 0

mkdir -p "$WHISPER_DIR/archive" 2>/dev/null || true

CONTEXT=""
COUNT=0
LAST_FROM=""
PENDING=0

HOP_MAX="${WHISPER_HOP_MAX:-5}"

for i in "${!MSG_FILES[@]}"; do
  msg_file="${MSG_FILES[$i]}"
  [ ! -f "$msg_file" ] && continue

  # G3: drop messages exceeding the hop budget (loop protection)
  HOP=$(jq -r '.hop_count // 0' "$msg_file" 2>/dev/null || echo 0)
  if [ "$HOP" -gt "$HOP_MAX" ]; then
    mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
    continue
  fi

  FROM=$(jq -r '.from // "unknown"' "$msg_file" 2>/dev/null || echo "unknown")
  CONTENT=$(jq -r '.content // ""' "$msg_file" 2>/dev/null || echo "")
  TS=$(jq -r '.timestamp // ""' "$msg_file" 2>/dev/null || echo "")
  THREAD=$(jq -r '.thread // ""' "$msg_file" 2>/dev/null || echo "")

  SHORT_TS=$(echo "$TS" | sed 's/.*T\([0-9]*:[0-9]*\).*/\1/')
  THREAD_TAG=""
  [ -n "$THREAD" ] && THREAD_TAG=" [${THREAD}]"

  CONTENT_LEN=${#CONTENT}

  # Build body: inline if small enough, reference otherwise
  if [ "$CONTENT_LEN" -lt "$INLINE_MAX" ]; then
    BODY="
> **${FROM}** (${SHORT_TS})${THREAD_TAG}: ${CONTENT}"
  else
    ARCHIVE_PATH="$WHISPER_DIR/archive/$(basename "$msg_file")"
    # Preview: strip newlines, keep first PREVIEW_LEN chars
    PREVIEW=$(printf '%s' "$CONTENT" | tr '\n' ' ' | tr -s ' ')
    PREVIEW="${PREVIEW:0:$PREVIEW_LEN}"
    BODY="
> **${FROM}** (${SHORT_TS})${THREAD_TAG}: [📂 ${CONTENT_LEN} chars — Read ${ARCHIVE_PATH}]
> Preview: ${PREVIEW}…"
  fi

  # Budget check: stop if adding this body would overflow, BUT always render the first message
  PROJECTED=$(( ${#CONTEXT} + ${#BODY} ))
  if [ "$COUNT" -gt 0 ] && [ "$PROJECTED" -gt "$OUTPUT_MAX" ]; then
    PENDING=$(( TOTAL_MSGS - i ))
    break
  fi

  CONTEXT="${CONTEXT}${BODY}"
  COUNT=$((COUNT + 1))

  # Track last valid sender for whisper-reply
  if echo "$FROM" | grep -qE '^[a-zA-Z0-9-]+$'; then
    LAST_FROM="$FROM"
  fi

  # Archive ONLY after successful rendering
  mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
done

[ "$COUNT" -eq 0 ] && exit 0

# Persist last sender for whisper-reply
if [ -n "$LAST_FROM" ]; then
  mkdir -p "$WHISPER_DIR/state" 2>/dev/null || true
  echo "$LAST_FROM" > "$WHISPER_DIR/state/last-sender-$PEER_ID" 2>/dev/null || true
  chmod 600 "$WHISPER_DIR/state/last-sender-$PEER_ID" 2>/dev/null || true
fi

# Header: show displayed vs pending when overflow
if [ "$PENDING" -gt 0 ]; then
  HEADER="━━━ 📨 whisper — ${COUNT} affiché(s), ${PENDING} en attente ━━━"
  CONTEXT="${CONTEXT}
> _(${PENDING} message(s) restant(s) — seront affichés au prochain prompt)_"
else
  HEADER="━━━ 📨 whisper — ${COUNT} message(s) ━━━"
fi

echo "${HEADER}${CONTEXT}
━━━
INSTRUCTION: Affiche ces messages whisper à l'utilisateur AVANT de répondre à son prompt. Utilise le format markdown ci-dessus tel quel. Si un message référence un chemin (Read /path/…), lis-le seulement si pertinent au prompt courant.
PRIORITY: Si tu es en milieu d'une tâche, termine-la avant de répondre par whisper — sauf si le contenu du whisper est directement pertinent pour la tâche en cours (auquel cas intègre-le). Ne bifurque jamais sur un whisper non lié au sujet de ton travail actuel."
