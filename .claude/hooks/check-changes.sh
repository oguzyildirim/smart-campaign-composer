#!/usr/bin/env bash
set -u

if ! command -v jq >/dev/null 2>&1; then
  echo "Warning: jq is not installed; post-edit checks were skipped." >&2
  exit 1
fi

INPUT="$(cat)"

FILE_PATH="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // ""')"

# Only format Go files.
if [[ "$FILE_PATH" == *.go && -f "$FILE_PATH" ]]; then
  if ! command -v gofmt >/dev/null 2>&1; then
    echo "Warning: gofmt is not installed; formatting was skipped." >&2
    exit 0
  fi

  # Exit 2 feeds gofmt errors (e.g. syntax errors) back to Claude.
  if ! gofmt -w "$FILE_PATH"; then
    echo "gofmt failed for $FILE_PATH; fix the errors above." >&2
    exit 2
  fi
fi

exit 0
