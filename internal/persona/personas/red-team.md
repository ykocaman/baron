---
id: red-team
name: Red team
description: 'Daily sweep: checks dependencies against known CVEs and comments findings on affected beads.'
model:
    tier: expert
trigger:
    schedule: 0 9 * * *
authority:
    bd_write: true
    actions:
        - comment
        - create
enabled: false
source: builtin
---
- Check the project's dependencies against known CVEs.
- If you find something: comment on the affected bead, or open a new bead.
