---
"ftw": patch
---

A stopped car starts again once the meter has shown room on every phase for the usual restart wait. The start used to add its new current to the fuse heat left by the car's own earlier charging, and after a stop on a busy phase it waited over a minute and then started low. Now it reads each phase as it is, plus the car's new current. The heat holds a start back only while it is itself over the working limit, after a real overload.
