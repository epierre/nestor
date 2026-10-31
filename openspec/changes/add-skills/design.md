# Design: skills

## Layout and configuration

```
skills/
  weekly-review/
    SKILL.md            # front matter: name, description; body: instructions
    checklist.md        # reference file, loaded on demand
  newsletter-triage/
    SKILL.md
    scripts/score.py    # only runnable if scripts are enabled for this skill
```

```yaml
# config/skills.yaml
skills:
  - name: weekly-review
    hash: sha256:3f1c...        # over all files in the folder, sorted by path
    users: ["*"]
    scripts: false
  - name: newsletter-triage
    hash: sha256:9a07...
    users: [1]
    scripts: true
    script_limits: { timeout: 20s, memory: 256m, max_output_bytes: 32768 }
```

`nestor skills pin <name>` computes and prints the hash after review. At startup, a skill
whose files do not match its pinned hash is skipped and logged; the process does not fail, so a
bad edit to one skill cannot take the assistant down.

## Progressive disclosure

The system prompt gets a short block per enabled skill for the requesting user:

```
Available skills (call load_skill to use one):
- weekly-review: Run my Sunday weekly review of tasks and calendar.
- newsletter-triage: Sort newsletters into read-now, later, and unsubscribe.
```

Descriptions are capped (200 characters) and the block is capped in total, so adding skills
cannot crowd out the conversation. The body is only loaded by `load_skill`, and its content is
returned as a tool message marked as skill instructions from a reviewed source.

## Built-in tools

| Tool | Risk | Behaviour |
|---|---|---|
| `load_skill(name)` | read | Returns `SKILL.md` body for a skill enabled for this user |
| `read_skill_file(name, path)` | read | Returns a reference file inside that skill's folder; rejects `..`, absolute paths and symlinks |
| `run_skill_script(name, script, args)` | reversible | Runs a bundled script in the sandbox; only if `scripts: true` |

`run_skill_script` is `reversible`, not `read`, because it executes code; it cannot reach any
data or network, so its only effect is its output.

## Sandbox

Each run starts a fresh container from `skill-runner` via the Docker API:
`--network none`, read-only root filesystem, the skill folder mounted read-only, a tmpfs work
dir, non-root user, CPU/memory/pid limits, killed after the timeout, removed afterwards.
Arguments are passed as a JSON file in the work dir, never interpolated into a shell command.

Giving the agent access to the Docker socket is itself a risk (it is root-equivalent on the
host). To contain it, the agent talks to a small `sandboxd` sidecar that holds the socket and
exposes exactly one operation: run this skill's script with these limits. The agent never gets
the socket.

## Failure modes

- Hash mismatch: skill skipped, logged; others unaffected.
- Script timeout or OOM: tool result describes the failure; run recorded with status error.
- `sandboxd` down: `run_skill_script` returns an error result; instruction-only skills still work.
- Model asks for a skill not enabled for the user: error result "unknown skill", same as non-existent.

## Dependencies

- A YAML front matter parser (`gopkg.in/yaml.v3`).
- `sandboxd`: in-repo Go binary using the Docker Engine API client (`github.com/docker/docker/client`).

## Testing approach

- Unit: parsing, hashing, path validation, prompt block assembly with caps.
- Integration: `sandboxd` against a real Docker daemon (tag `integration`).
- Evals: a test skill whose instructions tell the agent to send a message without approval and
  to read files outside its folder.
