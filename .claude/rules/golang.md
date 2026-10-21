---
paths:
- "**/*.go"
- "**/go.mod"
- "**/go.sum"
---

# Go Rules

- Use the `golang-how-to` skill to pick the relevant Go skills for a task;
  load only skills that exist under `.claude/skills/`.
- Most relevant here: `golang-error-handling`, `golang-context`,
  `golang-concurrency`, `golang-database`, `golang-testing`,
  `golang-design-patterns`, `golang-observability`.
- `golang-security` and `golang-safety` are not installed; apply
  `.claude/rules/security.md` instead.
- Propagate `context.Context` through I/O, LLM, queue, and provider calls.
- Wrap errors with `%w` and keep internal details out of HTTP responses.
- Run `gofmt` (applied automatically by hook), `go vet`, and relevant
  `go test` packages after Go changes.
