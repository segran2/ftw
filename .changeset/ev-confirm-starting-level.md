---
"ftw": patch
---

Wait for a confirmed current battery level before scheduling an EV from an assumed plug-in level. Keep older car readings visible with age and offer a one-action confirmation or slider correction. Reject cached automatic dispatch while confirmation is required; explicit manual and PV-only charging remain available.

Label the household battery projection explicitly so it cannot be mistaken for the EV battery level.

Show the current car target SoC after the charging timeline’s replanning notice, and update it when the charging goal changes. Keep the target visible even when no charging is planned, including when the current battery level already exceeds the goal.

Compare the car SoC target with its current level using ≥ or <; mark older readings and FTW estimates, and avoid comparing an unconfirmed assumed starting level.

Label current car telemetry From Car · Current and a manually confirmed level Confirmed by user. Retain manual provenance within a saved hardware session, and switch to FTW estimate once delivered energy advances the level.
