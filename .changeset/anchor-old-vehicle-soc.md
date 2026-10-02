---
"ftw": patch
---

A car reading up to an hour old now corrects FTW's charge estimate. FTW anchors the reading at the time it arrived and adds the energy delivered since then. Cloud sources such as the VW Group portal, which reports every 15 minutes, now keep the estimate close to the car's level. Before, FTW used a car reading only in its first five minutes. It also pinned the estimate to the latest reading and dropped the energy delivered after it. A reading from before plug-in, or from before a restart, does not anchor. Display and goal completion still treat readings older than five minutes as old.
