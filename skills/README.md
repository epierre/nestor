# Skills

One folder per skill, named after the skill. Each holds a `SKILL.md` (front matter with `name`
and `description`, then the instructions) and optional reference files.

A skill is used only once it is listed in [`config/skills.yaml`](../config/skills.yaml) with the
hash printed by `nestor skills pin <name>`. Any later edit to the folder disables the skill until
it is reviewed and pinned again. Symbolic links are not allowed.

This file is not part of any skill and is never read by Nestor.
