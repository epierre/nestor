# Add skills

## Why

Skills package know-how ("how I like my weekly review done", "how to triage newsletters") as a
folder of instructions the agent loads only when relevant. They let the assistant gain
behaviours without code changes or a bigger system prompt, and third-party skills in the open
`SKILL.md` format can be reused. Like MCP servers, third-party skills are untrusted until
reviewed: instructions can try to steer the agent, and bundled scripts are arbitrary code.

## What changes

- A skills directory (`skills/`, vendored in the repo) where each skill is a folder with a
  `SKILL.md` (front matter `name` and `description`, then instructions) and optional reference
  files and scripts.
- A `skills.yaml` that lists enabled skills, their pinned content hash, and which users get them.
- Progressive disclosure: only the name and description of each enabled skill are added to the
  system prompt; a built-in `load_skill` tool (risk read) returns the full instructions on demand,
  and `read_skill_file` (risk read) returns a bundled reference file.
- An optional sandboxed script runner: `run_skill_script` executes a script bundled with an
  enabled skill in a throwaway container with no network and no access to host data. It is
  disabled by default and enabled per skill.
- Content pinning: a skill whose files no longer match the pinned hash is not loaded.
- Owner-authored skills live in the same directory and follow the same rules.

## Capabilities

- `skills` (new)

## Constitution principles touched

- 1 Approval in code: skills add no tools and cannot change risk levels; instructions that tell
  the agent to skip approval have no effect because enforcement is in code.
- 2 Untrusted content: skill content is reviewed and pinned; script output returns as an
  untrusted tool message.
- 4 Data stays on the host: the script sandbox has no network.
- 6 User-scoped: skills are enabled per user; script runs receive no user data except the
  arguments the model passes in that call.

## Non-goals

- Installing skills at runtime or from chat ("install this skill from a URL").
- Skills that bundle MCP servers or register new tools.
- Letting the assistant write or modify its own skills (a possible later change, with review).
- Network access for skill scripts.

## Impact

- New package `internal/skills`; new `skills/` directory and `config/skills.yaml`.
- New sandbox runner image `deploy/skill-runner/` (minimal image with the interpreters skills need).
- Depends on `add-tool-approval-gate`. Independent of `add-mcp-servers`.
