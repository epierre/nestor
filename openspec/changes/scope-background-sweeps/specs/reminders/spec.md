## MODIFIED Requirements

### Requirement: Reminders are user-scoped in delivery
The scheduler MUST list active users from the identity directory rather than querying reminders across users, MUST fetch each active user's due reminders with that user in context, and MUST NOT deliver a reminder to a chat other than the one it was created in.

#### Scenario: Two users due at once
- **WHEN** two users each have a reminder due at the same tick
- **THEN** each reminder is delivered only to its creator's chat, and neither user receives the other's text
