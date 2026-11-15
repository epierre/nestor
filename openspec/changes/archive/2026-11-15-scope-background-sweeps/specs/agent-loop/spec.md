## MODIFIED Requirements

### Requirement: User isolation
The system MUST NOT include any data belonging to one user in a model request, reply or query result made for another user, and every store operation on user data MUST be scoped to a user ID; background sweeps SHALL list active users from the identity directory first and invoke user-data store operations separately for each user.

#### Scenario: Two users talk at the same time
- **WHEN** user A says "my code word is falcon" and user B then asks "what is my code word?"
- **THEN** user B's model request contains no message from user A

#### Scenario: Unscoped store call
- **WHEN** a store method for user data is called with a context that carries no user ID
- **THEN** it returns an error and performs no query

#### Scenario: Background sweep scopes user-table queries per user
- **WHEN** the stale-approval sweep or reminder scheduler runs with two active users
- **THEN** it lists active users first and calls user-data store methods separately for each user with only that user's ID in context
