# chat-interface Specification

## Purpose
Lets each allowed user talk to the assistant from their phone through Telegram, and keeps
everyone else out.

## Requirements

### Requirement: Allowlisted access
The system SHALL process messages only from Telegram identities on the configured allowlist and SHALL ignore messages from any other sender.

#### Scenario: Message from an allowlisted user
- **WHEN** an allowlisted user sends "hello" to the bot
- **THEN** the message is passed to the agent under that user's ID and a reply is sent to that user's chat

#### Scenario: Message from a stranger
- **WHEN** a Telegram user who is not on the allowlist sends a message
- **THEN** no reply is sent, no model call is made, and a warning with the sender ID is logged

#### Scenario: Identity removed from the allowlist
- **WHEN** an identity is removed from the allowlist and the process restarts, then that identity sends a message
- **THEN** the message is ignored and the user's stored data is kept

### Requirement: Identity resolution at the boundary
The system SHALL resolve each incoming Telegram sender identity to an internal user ID in the channel, MUST NOT pass platform user identities to the agent, and MAY attach an opaque chat routing address (`channel.WithChat`) to the request context solely so outbound cards, reminders and outcome notices return to the originating chat.

#### Scenario: Agent receives internal ID
- **WHEN** an allowlisted user sends a message
- **THEN** the agent is called with a context carrying the internal user ID and no Telegram user ID

#### Scenario: Originating chat address carried for outbound delivery
- **WHEN** an allowlisted user sends a message in a chat that triggers an approval card
- **THEN** the card's delivery chat address is recorded from the request context without exposing the sender's Telegram user ID to the agent

### Requirement: Text replies
The system SHALL send the agent's reply to the chat the message came from, splitting replies longer than Telegram's message size limit into consecutive messages in order.

#### Scenario: Long reply
- **WHEN** the agent produces a reply of 9000 characters
- **THEN** the user receives it as multiple messages, each within the limit, whose concatenation equals the reply

### Requirement: Unsupported message types
The system MUST reply with a short notice when a user sends a non-text message (photo, voice, file, sticker) and MUST NOT call the model for it.

#### Scenario: Voice message
- **WHEN** an allowlisted user sends a voice message
- **THEN** the user receives a notice that only text is supported and no model call is recorded

### Requirement: Start a new conversation
The system SHALL treat the `/new` command as closing the sending user's open conversation without calling the model, so the user's next message opens a fresh conversation with empty history, without affecting other users' conversations.

#### Scenario: User resets the conversation
- **WHEN** a user sends `/new` and then asks "what did I just say?"
- **THEN** `/new` makes no model call, and the model request for the next message contains no messages from before `/new`

#### Scenario: Reset does not affect another user
- **WHEN** user A sends `/new` while user B has an open conversation
- **THEN** user B's next model request still contains B's earlier messages

### Requirement: No message loss on transient failures
The system SHALL acknowledge a Telegram update only after the message has been handled or a failure reply has been sent.

#### Scenario: Crash during handling
- **WHEN** the process stops after receiving an update but before handling completes, and is restarted
- **THEN** the same update is received again and handled once
