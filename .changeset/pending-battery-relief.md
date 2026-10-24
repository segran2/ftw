---
"ftw": patch
---

Keep pending battery import and export increases reserved when ending existing fuse relief, so successive commands cannot spend the same phase headroom before the meter observes their effect.

Distribute retained relief only across available, previously commanded battery supply. A battery at its SoC floor or blocked in the required direction no longer consumes a share that another eligible battery can provide. Apply the same bounded allocation to import and export.
