# Smart Campaign Composer — Decisions

- Claude for development agent and try to create a loop for my SDLC cycle for this project.
    Analyze -> To-Spec -> Implement(TDD) -> Code-Review (This is mostly be inspired by https://github.com/mattpocock/skills). I also added golang skills to use it in Implement (https://github.com/samber/cc-skills-golang)
    Here I try to create a cycle like I normally do with developing Cursor in my previous jobs.

- Split each unit task by milestone in prompts.md file. (I used to-spec skill to create milestones)

  - Milestone 1 — Project foundation

  - Milestone 2 — User data and segmentation

  - Milestone 3 — AI campaign generation

  - Milestone 4 — Campaign approval and content

  - Milestone 5 — Queue and reliable delivery
  
  - Milestone 6 — Live monitoring

  - Milestone 7 — Hardening and submission

  - Milestone 8 — Scheduled campaign delivery (bonus)
 
- Use Golang for Backend and TypeScript + Vue for Frontend.  Since this project is sample project I try to use appropriate tools for job, I picked common, well-known tools that fit the job, rather than optimizing for scale. Postgres for datastore and Kafka for messaging.

- Simple package structure, cmd/ starts the programs, internal/ holds the main code, frontend/ is the website, migrations/ builds the database, mock-provider/ fakes SMS and email sending, docker-compose.yml runs everything together, Makefile has shortcut commands, and README.md explains how to use it.
  
- Try to cover every domain in one project, but I would split campaign, delivery, segmentation, content, LLM integration to different services.

- Created knowledge base under /docs with using OKF(https://github.com/GoogleCloudPlatform/open-knowledge-format)

Behaviour

- Human approval is required. The AI only suggests; a person must approve the exact content they reviewed, and the AI has no way to approve or send.

- The AI never writes SQL. It proposes a small allowlisted rule, which the backend checks and turns into safe parameterized SQL.

- All AI output is untrusted. It is strictly validated and gets one repair attempt if invalid.

- Transactional outbox. Approval saves the deliveries and their queue jobs in one database transaction, so a crash can't lose or half-send a campaign.

- Rate limit lives in Postgres. One shared token bucket per channel keeps all workers under 100 messages/second without adding another service. (I would use Redis like tool here)

- Server-Sent Events instead of WebSockets. Updates only flow from server to browser, so the simpler option was enough.

- Created mock service to just send SMS/MAIL, injects failures (errors, 429s, delays, lost responses).

- Achieve simple scheduled delivery and retry mechanism, Kafka carries delivery jobs; retries and scheduling are kept in Postgres instead of separate retry or dead-letter topics.

Notes

- Record each ai-sessions in /ai-sessions and also record technical decisions made with Claude(ai-sessions/DECISIONS.md)

- I also used project.md file original case specification its gitignored.




