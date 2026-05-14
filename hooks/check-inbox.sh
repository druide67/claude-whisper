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

# Sentinel writers: surface conditions that would otherwise exit 0 silently
# (bug PRISM-1 2026-05-13: .whisper-peer missing on PRISM for 12 days
# with zero alerts — 7 whispers stale). Each sentinel is keyed by sha-cwd
# so a looping hook doesn't flood the warnings dir. whisper-list / whisper-doctor
# read these files to surface the issue to the operator.
WARN_DIR="$WHISPER_DIR/state/warnings"
SHA_CWD=$(printf '%s' "$PWD" | shasum -a 256 2>/dev/null | cut -c1-12)

_write_warn() {
  # _write_warn <filename-without-dir> <kind> <json-payload-fragment>
  local fname="$1"; local kind="$2"; local payload="$3"
  mkdir -p "$WARN_DIR" 2>/dev/null || return 0
  local f="$WARN_DIR/${fname}"
  local now
  now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  printf '{"ts":"%s","cwd":"%s","kind":"%s",%s}\n' \
    "$now" "$PWD" "$kind" "$payload" > "$f.tmp" 2>/dev/null && \
    chmod 600 "$f.tmp" 2>/dev/null && \
    mv "$f.tmp" "$f" 2>/dev/null || rm -f "$f.tmp" 2>/dev/null
}

# CWD-scoped warnings (cleared automatically when the hook runs cleanly from
# the same CWD): missing-peer, invalid-peer.
write_warn() {
  local kind="$1"; local payload="$2"
  _write_warn "${kind}-${SHA_CWD}.warn" "$kind" "$payload"
}

clear_warn() {
  local kind="$1"
  rm -f "$WARN_DIR/${kind}-${SHA_CWD}.warn" 2>/dev/null || true
}

# Message-scoped warnings (persist until reviewed by operator via
# whisper-doctor): hop-overflow drops.
write_msg_warn() {
  local kind="$1"; local msg_basename="$2"; local payload="$3"
  _write_warn "${kind}-${msg_basename}.warn" "$kind" "$payload"
}

# Read peer-id from project directory
if [ ! -f ".whisper-peer" ]; then
  # Heuristic: suggest peer-id if basename(CWD) appears as a directory in inbox/
  SUGGESTED=""
  BASENAME_CWD=$(basename "$PWD")
  if [ -d "$WHISPER_DIR/inbox/$BASENAME_CWD" ]; then
    PENDING=$(find "$WHISPER_DIR/inbox/$BASENAME_CWD" -name 'msg-*.json' 2>/dev/null | head -1)
    [ -n "$PENDING" ] && SUGGESTED="$BASENAME_CWD"
  fi
  if [ -n "$SUGGESTED" ]; then
    write_warn "missing-peer" "\"suggested_peer\":\"$SUGGESTED\""
  fi
  exit 0
fi
PEER_ID=$(cat ".whisper-peer")

# Validate peer-id (prevent path traversal)
if ! echo "$PEER_ID" | grep -qE '^[a-zA-Z0-9-]+$'; then
  RAW_ESCAPED=$(printf '%s' "$PEER_ID" | head -c 80 | tr -d '"\\')
  write_warn "invalid-peer" "\"raw_content\":\"$RAW_ESCAPED\""
  exit 0
fi

# Reaching here = we have a valid .whisper-peer + peer-id, so any prior
# missing/invalid sentinel from this CWD is stale → clean up.
clear_warn "missing-peer"
clear_warn "invalid-peer"

INBOX="$WHISPER_DIR/inbox/$PEER_ID"
PROCESSING="$WHISPER_DIR/run/processing"

# Collect candidate files: own inbox + any processing/ msg destined to us.
# The processing/ scan is a robustness fallback: the v1.0 leader (extension
# VS Code) stages messages there, and crashes/disconnects/disabled extensions
# could otherwise leave them stuck forever (observed 2026-05-09/10, 41 stuck).
shopt -s nullglob
MSG_FILES=()
[ -d "$INBOX" ] && MSG_FILES+=("$INBOX"/msg-*.json)
if [ -d "$PROCESSING" ]; then
  for f in "$PROCESSING"/msg-*.json; do
    [ -f "$f" ] || continue
    TO=$(jq -r '.to // ""' "$f" 2>/dev/null || echo "")
    [ "$TO" = "$PEER_ID" ] && MSG_FILES+=("$f")
  done
fi
shopt -u nullglob
TOTAL_MSGS=${#MSG_FILES[@]}
[ "$TOTAL_MSGS" -eq 0 ] && exit 0

mkdir -p "$WHISPER_DIR/archive" 2>/dev/null || true

CONTEXT=""
COUNT=0
LAST_FROM=""
PENDING=0

HOP_MAX="${WHISPER_HOP_MAX:-8}"
HOP_HARD="${WHISPER_HOP_HARD:-20}"

for i in "${!MSG_FILES[@]}"; do
  msg_file="${MSG_FILES[$i]}"
  [ ! -f "$msg_file" ] && continue

  # G3: hop budget. Two thresholds — soft (HOP_MAX) renders the message with
  # a visible warning so the recipient knows a thread is looping; hard
  # (HOP_HARD) silently drops to actually break a runaway loop. The previous
  # behavior was to silently drop at HOP_MAX, which lost messages without
  # signal (asiai 2026-05-10 02:00 — msg-1778369832 with hop=6 archived
  # without ever being shown to the recipient).
  HOP=$(jq -r '.hop_count // 0' "$msg_file" 2>/dev/null || echo 0)
  if [ "$HOP" -gt "$HOP_HARD" ]; then
    # Hard drop is intentional (anti-runaway loop) but must NOT be invisible.
    # Surface as a warning so the operator can review what was dropped.
    MSG_ID_DROPPED=$(jq -r '.id // ""' "$msg_file" 2>/dev/null || echo "unknown")
    MSG_FROM_DROPPED=$(jq -r '.from // ""' "$msg_file" 2>/dev/null || echo "unknown")
    MSG_THREAD_DROPPED=$(jq -r '.thread // ""' "$msg_file" 2>/dev/null || echo "")
    ARCH_DROPPED="$WHISPER_DIR/archive/$(basename "$msg_file")"
    write_msg_warn "hop-overflow" "$(basename "$msg_file" .json)" \
      "\"msg_id\":\"$MSG_ID_DROPPED\",\"from\":\"$MSG_FROM_DROPPED\",\"thread\":\"$MSG_THREAD_DROPPED\",\"hop\":$HOP,\"archive\":\"$ARCH_DROPPED\""
    mv "$msg_file" "$WHISPER_DIR/archive/" 2>/dev/null || true
    continue
  fi
  HOP_WARN=""
  if [ "$HOP" -gt "$HOP_MAX" ]; then
    HOP_WARN=" ⚠ HOP=${HOP}/${HOP_MAX} (boucle suspectée)"
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
> **${FROM}** (${SHORT_TS})${THREAD_TAG}${HOP_WARN}: ${CONTENT}"
  else
    ARCHIVE_PATH="$WHISPER_DIR/archive/$(basename "$msg_file")"
    # Preview: strip newlines, keep first PREVIEW_LEN chars
    PREVIEW=$(printf '%s' "$CONTENT" | tr '\n' ' ' | tr -s ' ')
    PREVIEW="${PREVIEW:0:$PREVIEW_LEN}"
    BODY="
> **${FROM}** (${SHORT_TS})${THREAD_TAG}${HOP_WARN}: [📂 ${CONTENT_LEN} chars — Read ${ARCHIVE_PATH}]
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

# Heartbeat: update peers.json[PEER_ID].last_seen so whisper-list / whisper-doctor
# can detect peers whose hook hasn't run in a while. Best-effort, atomic write.
PEERS_FILE="$WHISPER_DIR/peers.json"
if [ -f "$PEERS_FILE" ]; then
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq --arg id "$PEER_ID" --arg ts "$NOW" \
    '.peers[$id].last_seen = $ts' \
    "$PEERS_FILE" > "$PEERS_FILE.tmp" 2>/dev/null && \
    chmod 600 "$PEERS_FILE.tmp" 2>/dev/null && \
    mv "$PEERS_FILE.tmp" "$PEERS_FILE" 2>/dev/null || \
    rm -f "$PEERS_FILE.tmp" 2>/dev/null
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
