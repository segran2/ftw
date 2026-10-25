---
"ftw": patch
---

A driver's rotated sign-in token, a settings save and a vehicle wake reservation no longer fail at once with "database is locked (SQLITE_BUSY)" while another part of FTW writes to the database. They now wait up to five seconds for it, as other writes already did. A MyUplink token lost this way could stop the driver signing in after the next restart.
