---
"ftw": patch
---

A car the plan runs at a fixed step for a quarter no longer stops a few seconds early. On mains above 230 V the car draws a little more than its step, so it used up the quarter's energy early. The stop then reached the charger in the next quarter, and on a busy phase the start test kept the car off for over a minute. A car now runs for the plan's charging time in the quarter, or until the energy is used up if that comes later. Core no longer sends a pause shorter than 20 seconds at the end of a quarter.
