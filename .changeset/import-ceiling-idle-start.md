---
"ftw": patch
---

An idle car now starts under the hourly import limit when the hour has room for it. The limit is an hourly average, but FTW let an idle car start only if the house and the car's lowest charging power fit under the limit at that moment. With a 4.5 kW limit, about 1 kW of house load and a car that needs 4.1 kW, the car stayed at 0 W all hour although the hour had over 3 kWh to spare. When the house and the car together need more than the limit, the car now starts once the hour has saved enough room, charges to the end of the hour and stops when the next hour begins, so each hour still averages under the limit. On such a site the charger starts and stops about once an hour. In the last 15 minutes of an hour FTW starts a car only if it fits under the limit itself, so it does not start a car only to stop it minutes later.
