---
type: Flow
title: Campaign flow
description: How a campaign moves from a natural-language brief to an explicitly approved, validated campaign.
tags: [campaign, ai, approval]
---

# Steps

Open http://localhost:5173, choose **New campaign**, and paste the example
brief:

> Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki
> kullanıcılara %30 indirim hatırlatması gönder. Samimi ama kısa olsun.

1. **Audience.** The AI proposes a [segment rule](../api/segment-preview.md);
   the backend validates it and counts matching users and consented
   recipients per channel. Criteria the rules cannot express are shown as a
   warning.
2. **Drafts.** *Generate SMS and email drafts* streams the text field by
   field. Partial text is display-only; drafts are stored only after they
   pass validation. Closing the tab cancels the model call.
3. **Edit and preview.** Edits autosave. The preview renders each selected
   channel's content for sample recipients, including the one with the
   longest first name, and shows final lengths after personalization and
   every problem approval would reject.
4. **Approve.** *Approve…* asks for your name and approves exactly the
   version and content hash you reviewed, to **send now** or on a
   **schedule** (see [Scheduled delivery](scheduled-delivery.md)). The
   campaign is then read-only. Sending now creates the deliveries in the
   same transaction (see [Delivery](delivery.md)).

The HTTP details are in [Campaigns](../api/campaigns.md).

# Statuses

`draft → audience_ready → drafts_ready → approved → sending → completed`,
or with a schedule `drafts_ready → scheduled → approved → …` (or
`release_failed` / `expired`). The backend owns them; the UI only displays
them. `sending` starts when a worker claims the first delivery; `completed`
means every delivery is `sent` or `failed`.

# Mock vs real mode

With `ANTHROPIC_API_KEY` empty (or `LLM_PROVIDER=mock`), a keyword-based
mock answers. It recognizes the example brief's city, cart, and no-purchase
phrases, a few others (Turkish and English), and age/gender/income/birthday
as unsupported. It returns fixed Turkish or English drafts, streamed in
chunks paced by `LLM_MOCK_STREAM_DELAY` so the UI shows them arriving.

With a key set, the API calls Claude (`ANTHROPIC_MODEL`, default
`claude-sonnet-5-5`); restart the API after changing `.env`
(`docker compose up -d api`). The startup log line shows `llm_provider` and
`llm_model`, never the key.

# What is validated

Model output is untrusted.

- It must match the JSON contract exactly (no unknown, missing, or
  duplicate keys; size caps 16 KB / 32 KB).
- The rule must pass the same validator as hand-written rules before any
  count runs.
- Drafts must be plain text (no HTML or control characters) and may use
  only `{{first_name}}` and `{{city}}`.
- Limits are checked on the worst case, with `first_name` counted as 30
  characters and `city` as 20: SMS ≤ 160, email subject ≤ 80, email body
  ≤ 2000 characters.
- If output fails validation, the model gets one repair attempt with the
  validation errors (`LLM_MAX_ATTEMPTS`).
- The brief is limited to 1000 characters.

# SMS length

SMS length is counted in characters (runes), as the specification asks.
Real networks count differently: any character outside the GSM-7 alphabet
(ş, ğ, ı, İ, emoji) switches the message to UCS-2, where one segment holds
only 70 characters. A 160-character Turkish SMS can therefore be billed as
several segments.
