#!/usr/bin/env bash
set -u

# Fail closed: without jq the tool input cannot be inspected.
if ! command -v jq >/dev/null 2>&1; then
  echo "Blocked: jq is not installed, so protect-files.sh cannot verify this call. Install jq (sudo apt install jq)." >&2
  exit 2
fi

INPUT="$(cat)"

TOOL_NAME="$(printf '%s' "$INPUT" | jq -r '.tool_name // ""')"
COMMAND="$(printf '%s' "$INPUT" | jq -r '.tool_input.command // ""')"
FILE_PATH="$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // ""')"

# Protect secret files and Git internals from direct Write/Edit operations.
if [[ "$TOOL_NAME" =~ ^(Write|Edit|MultiEdit)$ ]]; then
  case "$FILE_PATH" in
    */.env|.env|*/.env.*|.env.*)
      if [[ "$FILE_PATH" != ".env.example" &&
            "$FILE_PATH" != */.env.example ]]; then
        echo "Blocked: do not edit secret environment files through Claude tools." >&2
        exit 2
      fi
      ;;
    */.git/*|.git/*)
      echo "Blocked: do not directly edit Git internal files." >&2
      exit 2
      ;;
  esac
fi

# Apply a conservative denylist to shell commands.
if [[ "$TOOL_NAME" == "Bash" ]]; then
  SEP='(^|[;&|(`[:space:]])'
  END='([[:space:];&|)]|$)'

  DESTRUCTIVE=(
    "${SEP}git[[:space:]]+push([[:space:]].*)?[[:space:]](-f|--force[a-z-]*)${END}"
    "${SEP}git[[:space:]]+reset[[:space:]]+--hard"
    "${SEP}git[[:space:]]+clean[[:space:]]+-[a-zA-Z]*[fdx]"
    "${SEP}git[[:space:]]+(checkout|restore)[[:space:]]+(--[[:space:]]+)?\.${END}"
    "${SEP}rm[[:space:]]+-[a-zA-Z]*[rR][a-zA-Z]*[[:space:]]+(/|~|\\\$HOME|\.|\.git|\*|\./\*?)${END}"
    "docker[[:space:]]+compose[[:space:]]+down.*[[:space:]](-v|--volumes)${END}"
  )
  for pattern in "${DESTRUCTIVE[@]}"; do
    if printf '%s' "$COMMAND" | grep -Eq -- "$pattern"; then
      echo "Blocked: potentially destructive command. Review and execute it manually if intended." >&2
      exit 2
    fi
  done

  # Commands that would expose secret environment file contents.
  ENV_FILE='[^[:space:]]*\.env(\.local|\.[a-z]+\.local|\.production|\.development)?'
  SECRETS=(
    "${SEP}(cat|less|more|head|tail|sed|awk|grep|rg|bat|xxd|od|strings|base64)[[:space:]].*${ENV_FILE}${END}"
    "${SEP}(source|\.)[[:space:]]+${ENV_FILE}${END}"
    "${SEP}(printenv|env)${END}"
    "docker[[:space:]]+compose[[:space:]]+config"
  )
  for pattern in "${SECRETS[@]}"; do
    if printf '%s' "$COMMAND" | grep -Eq -- "$pattern"; then
      if [[ "$pattern" == *"docker"* && "$COMMAND" == *"--no-interpolate"* ]]; then
        continue
      fi
      echo "Blocked: avoid exposing secret environment values to Claude." >&2
      exit 2
    fi
  done
fi

exit 0
