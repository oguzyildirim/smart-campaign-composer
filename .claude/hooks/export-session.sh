#!/usr/bin/env bash
# SessionEnd hook: exports the session transcript to ai-sessions/ as Markdown.
# Re-exporting the same session (e.g. after --resume) overwrites its file.
set -u

if ! command -v jq >/dev/null 2>&1; then
  echo "export-session: jq is not installed; session was not exported." >&2
  exit 0
fi

INPUT="$(cat)"

TRANSCRIPT="$(printf '%s' "$INPUT" | jq -r '.transcript_path // ""')"
SESSION_ID="$(printf '%s' "$INPUT" | jq -r '.session_id // "unknown"')"
REASON="$(printf '%s' "$INPUT" | jq -r '.reason // "unknown"')"
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-$(printf '%s' "$INPUT" | jq -r '.cwd // "."')}"
HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="$PROJECT_DIR/ai-sessions"

[[ -f "$TRANSCRIPT" ]] || exit 0

BODY="$(jq -Rr -f "$HOOK_DIR/transcript-to-md.jq" "$TRANSCRIPT")"

# Skip sessions without any conversation (opened and closed immediately).
[[ "$BODY" == *"## 👤 User"* ]] || exit 0

mkdir -p "$OUT_DIR"

SHORT_ID="${SESSION_ID:0:8}"
OUT="$(find "$OUT_DIR" -maxdepth 1 -name "*_${SHORT_ID}.md" -print -quit)"
STARTED="$(jq -Rr 'fromjson? | .timestamp // empty' "$TRANSCRIPT" | head -n 1)"
ENDED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if [[ -z "$OUT" ]]; then
  STAMP="$(date -d "${STARTED:-now}" +%Y-%m-%d_%H%M 2>/dev/null || date +%Y-%m-%d_%H%M)"
  OUT="$OUT_DIR/${STAMP}_${SHORT_ID}.md"
fi

TMP="$(mktemp "$OUT_DIR/.export.XXXXXX")"
{
  echo "# Claude Code Session ${SHORT_ID}"
  echo
  echo "- Session ID: \`${SESSION_ID}\`"
  echo "- Started: ${STARTED:-unknown}"
  echo "- Ended: ${ENDED} (${REASON})"
  echo "- Exported automatically by \`.claude/hooks/export-session.sh\`"
  echo
  echo "---"
  echo
  printf '%s\n' "$BODY"
} | sed -E \
    -e 's/sk-ant-[A-Za-z0-9_-]{8,}/[REDACTED_ANTHROPIC_KEY]/g' \
    -e 's/(ANTHROPIC_API_KEY[[:space:]]*[=:][[:space:]]*)[^[:space:]"'"'"'`]+/\1[REDACTED]/g' \
    -e 's/(Bearer[[:space:]]+)[A-Za-z0-9._~+\/=-]{16,}/\1[REDACTED]/g' \
  > "$TMP" && chmod 644 "$TMP" && mv "$TMP" "$OUT"

exit 0
