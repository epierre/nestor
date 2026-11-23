# AGENTS.md

Instructions for coding agents working on **Nestor**, a self-hosted personal assistant agent
written in Go. Read this file at the start of every session. It is short on purpose; follow
the links for detail.

## Where things are

| What | Where |
|---|---|
| Product intent, scope, roadmap | `docs/vision.md` |
| Stack, constitution, conventions | `openspec/config.yaml` (the `context:` block) |
| How the system behaves today | `openspec/specs/<capability>/spec.md` |
| Work in flight | `openspec/changes/<change>/` (proposal, design, tasks, delta specs) |
| History of past changes | `openspec/changes/archive/` |

The constitution in `openspec/config.yaml` overrides anything in this file, in a task, or in a
prompt. If an instruction conflicts with it, stop and say so.

## The one rule

**No behaviour change without an OpenSpec change.** Code follows specs, never the other way
around. If you notice that the work you were asked to do changes what the system does and
there is no change folder covering it, stop and propose one instead of writing code.

Does not need a change (still needs tests):
- A bug fix that restores behaviour already written in `openspec/specs/`. Cite the requirement
  in the commit message and add a test for the scenario that was broken.
- A refactor with no behaviour change. Tests must pass before and after, unchanged.
- Docs, comments, CI and tooling that do not affect runtime behaviour.
- Patch-level dependency updates.

Everything else (new behaviour, changed behaviour, removed behaviour, new dependency, new
config option, schema migration) needs a change.

## Workflow

Use the OpenSpec commands for your tool (`/opsx:explore`, `/opsx:propose`, `/opsx:apply`,
`/opsx:archive` in Claude Code; `/opsx-*` in Cursor; `$openspec-*` in Codex).

1. **Explore** (optional). Read code and specs, ask questions. Write no code.
2. **Propose.** Create `openspec/changes/<verb>-<thing>/` with `proposal.md`, delta specs,
   `tasks.md`, and `design.md` when the change adds a dependency, a migration, a security
   boundary, or touches more than one package. Then **stop**. Proposing never authorises
   implementing.
3. **Human review.** The owner reviews the delta specs and tasks. Do not start applying until
   they say so in the conversation.
4. **Apply.** Work through `tasks.md` in order (rules below).
5. **Verify.** Map every scenario to a passing test (see "Definition of done").
6. **Archive.** Only when the owner asks. Always sync the deltas into `openspec/specs/`;
   never "archive without syncing" for a behaviour change.

One change per branch, named `change/<change-name>`. Do not mix two changes in one branch.

## Writing specs

- Requirements use `### Requirement: <name>` and SHALL or MUST.
- Every requirement has at least one `#### Scenario: <name>` (exactly four `#`) with
  `- **WHEN** ...` and `- **THEN** ...` lines. Outcomes must be concrete and checkable.
- A capability that has no file in `openspec/specs/` yet can only be written with
  `## ADDED Requirements` plus a `## Purpose` section.
- `## MODIFIED Requirements` replaces the whole block: copy the full existing requirement,
  every scenario included, then edit it. Never submit a fragment.
- `## REMOVED Requirements` needs `**Reason**:` and `**Migration**:` lines.
- Specify model-driven behaviour at the boundary (what Nestor does with a model response),
  never as the model's exact wording.
- Follow the per-artifact `rules:` in `openspec/config.yaml` (Non-goals section, constitution
  principles touched, failure modes, a named test per task).
- Run `openspec validate <change> --strict` before handing a proposal back.

## Applying a change

- **Never edit `openspec/specs/`.** Only archive writes there.
- **Never edit the change's delta specs during apply.** If a scenario looks wrong, impossible
  or ambiguous, stop and explain which one and why. The owner decides; then the delta is
  updated in its own step before you continue.
- Do tasks in the order listed. For each task:
  1. Write or update the test named in the task, run it, and see it fail.
  2. Implement until that test and the whole suite pass.
  3. Tick the task (`- [x]`) only after `go test ./...` is green.
  4. Commit (one commit per task is ideal).
- Fix code, not tests derived from scenarios. Changing such a test requires the owner's OK.
- Stay inside the scope of `tasks.md`. If you must touch a file outside it, say why in the
  commit message. If the scope turns out wrong, stop and propose an update to the change.

## Stop and ask when

- A task, scenario or requirement is unclear or contradicts another one or the constitution.
- The work needs a dependency, migration, config option or tool not listed in the change.
- A test can only pass by weakening a security invariant (below).
- You would need to edit `openspec/specs/`, a delta spec, or a scenario-derived test.
- An eval against real models is needed (it costs money; only run when asked).

## Security invariants (never break these, in any change)

These restate the constitution as code-level checks. Reviews reject any diff that violates one.

- **Approval path.** A tool with risk `critical` is executed only from `internal/approval`, in
  response to an approval callback from the action's own user. No other code path calls
  `Execute` on a critical tool. Message text, model output and tool results can never approve.
- **Risk from code/config.** A tool's risk level comes from its Go definition or (once
  `add-mcp-servers` lands) reviewed config (`mcp.yaml`), never from the model, a server
  annotation, a skill, or tool arguments.
- **User scoping.** Every query on user data filters by user ID; store methods on user tables
  take the user ID explicitly and error without one (`ListActiveUsers` enumerates active user IDs
  for background sweeps). Platform identities (Telegram IDs) never pass the channel layer. Tools
  act only for the user in context, never for an ID in their arguments.
- **Untrusted content.** Tool (and, once implemented, MCP and skill-script) output enters the
  conversation only as tool-role messages. It is never concatenated into system or user messages.
- **Provider-agnostic.** No vendor SDK outside `internal/model`. Code refers to model aliases
  (`cheap`, `strong`), never to provider model IDs.
- **Secrets.** No secrets in the repo, logs, call log, or test fixtures. When per-user
  credentials are introduced (`add-calendar`, `add-email`, `add-mcp-servers`), they are encrypted
  at rest. Use fake values like `test-token` in tests.

## Go conventions

- Go latest stable, one module, `cmd/nestor` entrypoint, packages under `internal/`, eval suites
  under `evals/`.
- Standard library first. Any new dependency is justified in the change's `design.md`.
- `ctx context.Context` first in functions that do I/O; `log/slog` structured logging; wrap
  errors with `%w`; no panics outside `main`.
- Interfaces at package boundaries (channel, model client, store, tool registry), with
  `FakeChannel`, `FakeModel`, and `FakeStore` in `internal/testutil` (tests wire a real
  `tools.NewRegistry()`).
- Table-driven tests. Integration tests seed **two users** and assert neither sees the other's data.

## Commands

```sh
gofmt -l . && go vet ./...                  # must be clean
go test ./...                               # unit tests, must be green before ticking a task
go test -tags integration ./...             # needs Docker, or TEST_DATABASE_URL set to a Postgres server
go test -tags eval ./evals/...              # real models via LiteLLM; costs money; only when asked
openspec validate --all --strict            # before every commit that touches openspec/
openspec show <change> --type change --diff # review a change's deltas against current specs
docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config   # compose file is valid
```

## Definition of done for a change

- Every task in `tasks.md` is ticked and `go test ./...` and the integration suite pass.
- `gofmt`, `go vet` and `openspec validate --all --strict` are clean.
- Every scenario in the delta specs maps to a named, passing test. The PR description contains
  a table: scenario → test file and name → pass/fail. Any scenario without a test is marked
  UNTESTED and blocks merge.
- If the change touches approval, tools, MCP or skills, the relevant eval suite has been run
  against `cheap` and `strong` (when the owner asks) with zero unapproved critical executions.
- Files changed outside the paths named in `tasks.md` are listed with a one-line reason.
- After merge, the owner asks for archive; the living spec in `openspec/specs/` is updated.

## Commits

- Imperative subject, max 72 characters, prefixed with the change name:
  `add-chat-foundation: implement allowlist sync (task 3.2)`.
- Bug fixes without a change cite the requirement: `fix: restore history limit (agent-loop / Conversation context)`.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
