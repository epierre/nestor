## MODIFIED Requirements

### Requirement: Approval card
The system SHALL send the approval card only to the user who owns the pending action, showing a preview generated from the stored arguments and, when needed by the tool, the requesting user's own stored data, with Approve and Reject controls bound to the pending action ID. When delivering an approval card fails, the system SHALL re-send the card on the owner's next message while the action remains proposed and unexpired.

#### Scenario: Card shows what will run
- **WHEN** a pending action for `notify_self` with text "Dentist at 3pm" is created for user A
- **THEN** user A receives a card whose text includes "Dentist at 3pm" with Approve and Reject buttons, and no other user receives it

#### Scenario: Preview uses the owner's stored item
- **WHEN** a critical tool builds its preview from the owner's stored data as well as the call arguments
- **THEN** the pending action stores the preview resolved under the owner's user ID in context

#### Scenario: Undelivered card retried on next message
- **WHEN** sending an approval card fails when the action is proposed and the owner sends another message before the action expires
- **THEN** the approval card for that pending action is sent once on the next message and not sent again on later messages
