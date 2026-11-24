## ADDED Requirements

### Requirement: Notification splitting
The system SHALL split outbound notifications sent via `Notify` that exceed Telegram's `4096` UTF-16 code unit limit into consecutive parts within the limit without splitting inside a Unicode character, and SHALL send each part in order as a single-attempt request without retry backoff.

#### Scenario: Oversized notification split into consecutive single-attempt sends
- **WHEN** `Notify` is called with text exceeding `4096` UTF-16 code units (such as a pre-existing oversized `OutcomeNoticeText` or a long MCP administrator alert)
- **THEN** Telegram receives consecutive `sendMessage` requests in order, each at most `4096` UTF-16 code units, whose concatenation equals the original text

#### Scenario: Notification send failure returns without backoff
- **WHEN** any `sendMessage` request during `Notify` fails
- **THEN** `Notify` returns an error immediately without retrying or sleeping for backoff
