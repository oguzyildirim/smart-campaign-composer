# AI Sessions

Claude Code session exports used to build this project.

## How exports are produced

A `SessionEnd` hook (`.claude/hooks/export-session.sh`) converts each
session transcript to Markdown when the session ends (exit, `/clear`,
logout). Files are named `YYYY-MM-DD_HHMM_<session-id-prefix>.md`;
resuming a session re-exports it to the same file.

Each export contains user prompts, Claude's replies, tool calls, and
truncated tool results. Internal system reminders, model thinking, and
subagent sidechains are omitted. Anthropic API keys and bearer tokens are
redacted automatically.

## Rules

- Exports are generated, never hand-written or edited to change content.
- Review each export for secrets or personal data before committing.
- Commit exports separately from application source changes.
