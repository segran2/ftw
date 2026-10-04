---
"ftw": patch
---

Phones can save household planner preferences over the session. `planner.prefs.set` stores the forecast safety factor and whether the battery may sell, and the box maps that permission to a planner mode. Each write sends only the preference it changes, so another client's stored choice stays in place.
