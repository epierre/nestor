## Purpose

Lets the owner ask to be reminded of something at a time, and delivers each reminder to the chat it
was asked in when it is due.

## ADDED Requirements

### Requirement: Reminders are delivered when due
The system SHALL send a reminder's text to the chat it was created in once it is due, and MUST NOT send a reminder that was cancelled or already delivered.

#### Scenario: Due reminder delivered
- **WHEN** a reminder "Call the dentist" becomes due
- **THEN** its text is sent to the chat it was created in, and it is marked delivered

#### Scenario: Delivery retried after a failed send
- **WHEN** sending a due reminder fails
- **THEN** the reminder remains undelivered and is sent on a later scheduler tick

### Requirement: Reminder times are read in the configured zone
The system SHALL read reminder times in the configured `TIMEZONE`, refuse times that are in the past or cannot be parsed, and show due times in that zone.

#### Scenario: Time read in the configured zone
- **WHEN** the model creates a reminder for "15:00" with `TIMEZONE` at its default, Europe/Zurich
- **THEN** the reminder is due at 15:00 in Europe/Zurich

#### Scenario: Past time refused
- **WHEN** the model creates a reminder for a time already past
- **THEN** no reminder is created and the model receives an error result

### Requirement: Cancelling a reminder requires approval
The system MUST treat cancelling a reminder as a critical action, carried out only after the owner approves it.

#### Scenario: Cancel approved
- **WHEN** the owner approves cancelling a pending reminder
- **THEN** the reminder is cancelled and is never delivered

### Requirement: Reminders are user-scoped in delivery
The scheduler MUST fetch each user's due reminders with that user in context, and MUST NOT deliver a reminder to a chat other than the one it was created in.

#### Scenario: Two users due at once
- **WHEN** two users each have a reminder due at the same tick
- **THEN** each reminder is delivered only to its creator's chat, and neither user receives the other's text
