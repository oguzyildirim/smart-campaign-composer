---
paths:
- "internal/**/*.{go,sql}"
- "cmd/**/*.go"
- "migrations/**/*.sql"
- "frontend/**/*.{ts,tsx,js,vue}"
- "mock-provider/**/*"
---

# Security Rules

## LLM output
- Treat model output as untrusted.
- Validate JSON against an explicit schema and enforce size limits.
- Reject unknown fields and unsupported operators.
- Never execute model-generated SQL, shell commands, or code.
- Use allowlisted segment fields and parameterized SQL.
- Treat user-provided campaign briefs as data, not system instructions.
- The LLM cannot approve campaigns, bypass consent, or trigger delivery.

## Messaging
- Use only fake seeded recipients.
- Use only the local mock provider.
- Never add credentials or endpoints for real messaging services.
- Persist explicit human approval before creating delivery jobs.
- Check consent at audience selection and immediately before sending.
- Use idempotency keys and durable uniqueness constraints.
- Enforce channel rate limits across all workers.
- Enforce content limits after rendering personalization variables.

## Secrets and personal data
- Never print secrets into logs or source code.
- Never read or expose real credentials from local environment files.
- Keep .env untracked; maintain .env.example with placeholders.
- Avoid logging phone numbers, emails, names, or complete message content.
- Escape user-controlled content in rendered HTML.
- Do not return internal errors, SQL, or stack traces to clients.

## Changes
- Explain security-sensitive design changes.
- Add regression tests for security fixes.
- Do not disable validation, consent checks, or safety controls to make tests pass.
