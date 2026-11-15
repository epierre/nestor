## MODIFIED Requirements

### Requirement: Cancelling a reminder requires approval
The system MUST treat cancelling a reminder as a critical action, carried out only after the owner approves it, and SHALL resolve only pending (undelivered and uncancelled) reminders owned by the requesting user when previewing or executing a cancellation.

#### Scenario: Cancel approved
- **WHEN** the owner approves cancelling a pending reminder
- **THEN** the reminder is cancelled and is never delivered

#### Scenario: Delivered or cancelled reminder treated as not found
- **WHEN** a reminder has already been delivered or cancelled and `reminder_cancel` previews or executes for its ID
- **THEN** the reminder is treated as not found (`no such reminder` in the preview and an error on execution) rather than shown as a pending reminder
