## 1. Loading and pinning

- [ ] 1.1 Parse `SKILL.md` front matter and body; parse `skills.yaml`. Test: `skills_test.go` "valid skill parsed", "missing name rejected", "description over cap rejected".
- [ ] 1.2 Folder hashing and `skills pin` command; skip on mismatch. Test: `skills_test.go` "hash stable across file order", "edited file skipped, others loaded".

## 2. Progressive disclosure

- [ ] 2.1 System prompt block of enabled skills per user, with per-description and total caps. Test: `prompt_test.go` "only enabled skills listed", "block within cap", "no instructions in prompt".
- [ ] 2.2 `load_skill` and `read_skill_file` tools with per-user checks and path confinement. Test: `tools_test.go` "load enabled skill", "other user's skill is unknown", "traversal rejected", "symlink rejected".

## 3. Script sandbox

- [ ] 3.1 `skill-runner` image and `sandboxd` sidecar exposing a single run operation with limits. Test: integration `sandboxd_test.go` "network unavailable", "read-only mount", "timeout kills and removes container", "arbitrary image refused".
- [ ] 3.2 `run_skill_script` tool (risk reversible) calling `sandboxd`, args via JSON file, output capped and recorded. Test: `tools_test.go` "scripts disabled returns error", "output truncated with marker", "run recorded".

## 4. Evals and wrap-up

- [ ] 4.1 Eval skill attempting to bypass approval and read outside its folder. Test: `go test -tags eval ./evals/skills/...` passes against `cheap` and `strong`, zero unapproved critical executions, zero out-of-folder reads.
- [ ] 4.2 Write one owner skill (`weekly-review`) and enable it. Test: manual smoke in Telegram ("do my weekly review") shows `load_skill` in the tool call log.
- [ ] 4.3 `openspec validate add-skills --strict` and `go test ./...` green.
