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

## Runtime paths

Two environment variables tell the running process where skills live, in the same way
`MCP_CONFIG` names `mcp.yaml`:

| Variable | Meaning |
|---|---|
| `SKILLS_CONFIG` | Path of the reviewed `skills.yaml`. Unset: no skills are offered and `skills/` is not read. |
| `SKILLS_DIR` | Folder holding one subfolder per skill. Required when `SKILLS_CONFIG` is set. |

`deploy/docker-compose.yml` mounts `config/skills.yaml` at `/etc/nestor/skills.yaml` and `skills/`
at `/etc/nestor/skills`, both read-only, and sets the two variables. The container's root
filesystem is already read-only, so nothing in the agent can edit a skill after it was pinned.

A `skills.yaml` that fails to parse or validate (unknown field, duplicate or invalid name,
malformed hash, unknown user) stops startup, as an invalid `mcp.yaml` does: it is a reviewed file
and a mistake in it should be seen at once. A single skill whose folder does not match its pin is
different: it is skipped and logged, and the process keeps running (see above).

`nestor skills pin <name>` is a review-time command run from the repository root and reads
`skills/<name>`; it does not use these variables.

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

## Size limits

Fixed in code, like the description cap, and checked while hashing a folder:

| File | Limit | Rationale |
|---|---|---|
| `SKILL.md` | 16 KiB (~4k tokens) | Paid on every `load_skill`; room for detailed instructions |
| Any other file | 32 KiB (~8k tokens) | Same order as the script output cap; bounds one `read_skill_file` result |

An oversized file makes the whole skill invalid: `nestor skills pin` refuses it and `Load` skips it
with an error, as for a hash mismatch. Nothing is truncated, because half of a skill's
instructions can be worse than none, and the owner learns about it at review time rather than
when the skill is used. Raising a limit is a code change.

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

### sandboxd

`sandboxd` (`cmd/sandboxd`, package `internal/sandbox`) decides on its own what it runs; the
agent only names a skill and a script:

- It reads the same `skills.yaml` and `skills/` as Nestor (`SKILLS_CONFIG`, `SKILLS_DIR`, mounted
  read-only), and listens on `SANDBOXD_ADDR` (default `:8080`) on the internal `sandbox` network.
  It checks the structure of `skills.yaml` but not user IDs: it has no database, and per-user
  enablement is checked by the agent's `run_skill_script` tool before it calls `sandboxd`.
- `POST /run` takes exactly `{"skill", "script", "args"}`; any other field (an image, a command,
  limits, mounts) is refused and logged, as is a body over 64 KiB. Before every run it hashes the
  skill folder again and runs only if the skill has `scripts: true`, still matches its pin, and
  `script` is a pinned file of that skill with a known interpreter (`.py` → `python3`, `.sh` →
  `sh`). It answers `{"exit_code", "output", "truncated", "timed_out", "duration_ms"}`.
- The image (`nestor-skill-runner`) and every container setting are constants in code. Limits come
  from `script_limits`, defaulting to timeout 20s, memory 256m and 32 KiB of output.
- Runs are serialised (one at a time), which keeps the stack inside its memory budget.

Container settings for each run: `NetworkMode: none`, read-only root filesystem, the skill folder
bind-mounted read-only at `/skill`, tmpfs `/work` and `/tmp` (16 MiB, `noexec,nosuid`), user
`65532:65532`, `CapDrop: ALL`, `no-new-privileges`, memory and swap at the limit, one CPU, 64
pids. After the timeout the container is killed; it is force-removed after every run, whatever
the outcome. Output is stdout and stderr interleaved, read at most to the cap.

Arguments are passed in the `NESTOR_ARGS` environment variable; the image's entrypoint writes it
to `/work/args.json` with `printf '%s'`, unsets it and `exec`s the interpreter, so arguments are
never parsed by a shell.

Bind mounts take host paths, but `sandboxd` sees the skills folder at its own mount point. At
startup it inspects its own container (its hostname is the container ID) and takes the host
source of the mount at `SKILLS_DIR`; this needs no extra setting. Outside a container, as in
tests, the folder is used as is.

The `skill-runner` image (`deploy/skill-runner/`) is `python:3.13-alpine` pinned by digest plus
the entrypoint. Compose builds it only on request (`docker compose --profile build build
skill-runner`) since no service runs it; `sandboxd` logs an error at startup if it is missing, and
each run then fails with an error result.

## Failure modes

- Hash mismatch: skill skipped, logged; others unaffected.
- Script timeout or OOM: tool result describes the failure; run recorded with status error.
- File over its size limit: skill refused by `skills pin`, skipped and logged at load.
- `skills.yaml` invalid, or `SKILLS_CONFIG` set without `SKILLS_DIR`: startup fails with an error
  naming the problem.
- `sandboxd` down: `run_skill_script` returns an error result; instruction-only skills still work.
- Model asks for a skill not enabled for the user: error result "unknown skill", same as non-existent.

## Dependencies

- `gopkg.in/yaml.v3` parses `skills.yaml` and the `SKILL.md` front matter. The standard library
  has no YAML parser, and a hand-written one would be a larger and riskier parser than this
  dependency. It is a widely used, pure-Go package with no transitive dependencies.
- No Docker client library. `sandboxd` uses six Docker Engine API calls (inspect, create,
  start, wait, logs, remove) over the Unix socket with `net/http`. The official client
  (`github.com/moby/moby/client`, formerly `github.com/docker/docker/client`) would add 19 modules,
  including OpenTelemetry and containerd packages, for those six calls. Shelling out to the
  `docker` CLI needs the same socket and adds argument-escaping risk.
- `sandboxd`: in-repo Go binary that holds the Docker socket and exposes one operation.

## Testing approach

- Unit: parsing, hashing, path validation, prompt block assembly with caps.
- Integration: `sandboxd` against a real Docker daemon (tag `integration`).
- Evals: a test skill whose instructions tell the agent to send a message without approval and
  to read files outside its folder.
