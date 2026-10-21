---
paths:
- "frontend/**/*"
---

# Frontend Rules

- Use Vue 3 Composition API and TypeScript with `strict` enabled.
- Keep components focused and reusable.
- Follow the existing component and state-management conventions.
- Avoid `any`; define explicit types for API responses and events.
- Keep API communication in a shared client or service layer.
- Handle loading, empty, error, and success states.
- Never store Claude API keys in frontend configuration.
- Treat all backend responses as untrusted data.
- Render user-controlled text safely; avoid v-html.
- Validate forms for usability, but rely on backend validation for security.
- Show final character counts after personalization where possible.
- Clean up SSE connections and event listeners on component unmount.
- Handle SSE errors and reconnections without duplicating displayed events.
- Prevent repeated approval submissions while a request is in progress.
- Keep campaign approval state controlled by the backend.
- Use accessible labels and keyboard-friendly controls.
- Add component tests for important user interactions.
