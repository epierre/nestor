## Purpose

Lets the owner keep a personal list of tasks by talking to the assistant, with deletion gated by
approval.

## ADDED Requirements

### Requirement: Tasks belong to one user
The system SHALL store each task with exactly one user ID, and every task operation MUST act only on tasks of the user in the request context.

#### Scenario: Another user's task ID
- **WHEN** a user asks to complete a task ID that belongs to another user
- **THEN** the system reports the task as not found, and the other user's task is unchanged

### Requirement: Adding and completing tasks
The system SHALL let the model add a task with a title of 1 to 200 characters, list open tasks, and mark the user's own task complete, without approval.

#### Scenario: Add and list
- **WHEN** the model adds a task "Renew passport" and then lists open tasks
- **THEN** the list includes "Renew passport" with its ID

#### Scenario: Due day stored and listed
- **WHEN** the model adds "Renew passport" due "2026-12-01" and then lists open tasks
- **THEN** the task is listed with due day 2026-12-01, and tasks without a due day come after the dated ones

#### Scenario: Invalid due day refused
- **WHEN** the model adds a task with the due day "2026-02-30"
- **THEN** no task is created and the model receives an error result

#### Scenario: Complete hides the task
- **WHEN** the user's task is completed and open tasks are listed
- **THEN** the task is absent from the list, and present when done tasks are requested

### Requirement: Deleting a task requires approval
The system MUST treat task deletion as a critical action: the task is deleted only after the owner approves the proposed deletion, and the proposal names the task title.

#### Scenario: Delete proposed
- **WHEN** the model asks to delete a task titled "Renew passport"
- **THEN** no task is deleted, and the owner's card names "Renew passport"

#### Scenario: Delete approved
- **WHEN** the owner approves the deletion
- **THEN** the task is deleted once and the outcome is reported to the owner
