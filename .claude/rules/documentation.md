---
paths:
- "README.md"
- "DECISIONS.md"
- ".env.example"
- "docker-compose.yml"
- "Makefile"
---

# Documentation Rules

- Document commands that actually work in the current repository.
- Keep README setup instructions aligned with Docker Compose.
- Explain required environment variables without including secrets.
- Record significant decisions, alternatives, and trade-offs in DECISIONS.md.
- Include relevant failure modes and operational constraints.
- Update documentation when APIs, configuration, or behavior changes.
- Do not claim a service, feature, or test exists unless verified.
- Do not overwrite useful existing documentation with generic boilerplate.
- Keep the README concise and focused on setup, architecture, usage,
  testing, and troubleshooting.
