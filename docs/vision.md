# Nestor: product vision

Nestor is a self-hosted personal assistant, named after the unflappable, discreet butler of
the Tintin stories (and the wise counsellor of Greek myth).

This is the "what and why" for the whole project. It deliberately contains no technology
choices (those live in `openspec/config.yaml` and each change's `design.md`).

## Purpose

A personal assistant, Nestor, that one person talks to in plain language, which manages their
tasks, appointments, reminders and email, gets better the more it is used, and never
takes a consequential action on their behalf without their explicit approval.

It is also a learning project: understanding how agents are built, hosted and evaluated
matters as much as the end result.

## Users

v1 serves exactly one person, the owner. The stories below are written for them.

Later, a few trusted people sharing the same host (a household) may each get their own
assistant on it. Each person's data, approvals, credentials and costs are fully separate.
v1 does not build this, but must not make it hard: every user-facing decision is made
per user, not globally. A public service for strangers is a different product and not
a goal.

## Core user stories

Conversation
- As the owner, I can message the assistant from my phone and get a reply within seconds.
- As the owner, the assistant remembers what we said earlier in the same conversation.

Tasks and reminders
- As the owner, I can add, list, complete and delete tasks by talking to the assistant.
- As the owner, I can ask to be reminded of something at a time, and the assistant messages me then.

Calendar
- As the owner, I can ask what is on my calendar for a day or week.
- As the owner, I can ask the assistant to create or move an event, and it asks me to confirm first.

Email
- As the owner, I can ask for a summary of new or important email.
- As the owner, I can ask the assistant to draft a reply; it shows me the draft and sends only when I approve.

Learning about the owner
- As the owner, the assistant learns stable facts and preferences from our conversations
  (people I mention, how I like meetings scheduled) and uses them later.
- As the owner, I can see what the assistant has learned about me, correct it, and make it forget things.

Control and trust
- As the owner, I approve, edit or reject every critical action (sending email, accepting
  invitations, deleting data) before it happens.
- As the owner, I can see what the assistant did, when, and what it cost.
- As the owner, I can switch which AI models are used, based on cost and availability,
  without code changes.

## Success criteria for v1

- The assistant is reachable 24/7 from my phone with a median reply time under 10 seconds.
- Zero critical actions executed without a recorded approval, verified by an eval suite
  that includes prompt-injection attempts in incoming email.
- Model spend per month is visible and stays under a budget I set.
- I use it daily for tasks and reminders for two consecutive weeks.

## Out of scope for v1

- Onboarding more users: signup, an admin UI, roles, per-user budgets or model choices.
- Sharing between users (shared calendars, delegated approvals) or a public service.
- Voice interface.
- Running chat models locally.
- Autonomous actions without approval for anything classed as critical.
- Payments, purchases, or anything touching money.
- Browsing arbitrary websites on the owner's behalf.

## Roadmap (one OpenSpec change each, in order)

1. `add-chat-foundation`: Telegram chat, agent loop, one model via an OpenAI-compatible proxy, conversation persistence, call logging.
2. `add-tool-approval-gate`: tool registry with risk levels, pending actions, approve/edit/reject flow.
3. `add-tasks-and-reminders`: first real tools (tasks, reminders) plus the scheduler.
4. `add-model-routing`: model tiers, fallbacks, cost tracking and budget alerts.
5. `add-calendar`: read calendar, propose event changes through the approval gate.
6. `add-email`: read and summarise mail, draft replies, send via approval.
7. `add-owner-memory`: profile facts, episodic recall, review/correct/forget commands.
8. `add-proactive-briefings`: morning summary and important-mail alerts.
9. `add-mcp-servers`: third-party MCP tools with configured risk levels, per-user enablement and credentials, pinned descriptions.
10. `add-skills`: reviewed, pinned skills loaded on demand, optional sandboxed scripts.
11. (Later, optional) `add-household-users`: onboarding further users, per-user budgets.

Changes 9 and 10 depend only on change 2 and can be pulled earlier whenever useful.

## Decisions

- Mail: chosen per user. The owner uses Proton Mail; other household users use Gmail.
- Calendar: Google Calendar for all users.
- Models: Gemini Flash for the cheap tier, Claude Opus 5.5 for the strong tier.

## Open questions

- Monthly model budget figure.
- Where Proton Mail Bridge runs (container on the N100 vs. separate) and how it is kept
  signed in unattended. Decide before `add-email`.
- Whether Gmail over IMAP is good enough (labels, search) or a Gmail API adapter is needed.
  Decide during `add-email`.
