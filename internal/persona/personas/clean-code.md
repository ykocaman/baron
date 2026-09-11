---
id: clean-code
name: Clean-code reviewer
description: Flags code that reimplements something a library already does simply, right after merge.
model:
    tier: standard
trigger:
    "on":
        - from: '*'
          to: merged
        - from: '*'
          to: closed
    issue_types:
        - bug
        - task
        - feature
authority:
    bd_write: true
    actions:
        - comment
        - create
enabled: false
source: builtin
---
- Look at the code that just merged in this bead.
- Find anything reimplemented from scratch that a library already solves simply.
- If you find something: comment on this bead, or open a separate follow-up bead.
