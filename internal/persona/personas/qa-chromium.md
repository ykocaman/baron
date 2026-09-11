---
id: qa-chromium
name: QA (Chromium)
description: Verifies a bead's change still works end to end in a live browser once it merges.
model:
    tier: fast
trigger:
    "on":
        - from: '*'
          to: merged
authority:
    bd_write: true
    actions:
        - reopen
        - comment
enabled: false
source: builtin
---
- Check this bead's merged change end to end in a real browser (Chromium).
- If it's broken: reopen the bead (back to 'open') and comment explaining what's broken and why.
- If it works: leave a short comment confirming it.
