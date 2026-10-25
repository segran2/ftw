---
"ftw": patch
---

The bundled Easee Cloud driver (1.4.0) reads Easee's live stream instead of asking the cloud every 5 seconds: charger state, plug-in and power arrive within about 2 seconds, and the cloud is asked once a minute only to confirm. If the stream drops, the driver falls back to asking every 5 seconds as before. The bundled myUplink driver (1.2.4) retries a slow read after 2 seconds instead of 30, so one slow answer from myUplink no longer marks the heat pump offline, and a renewed sign-in is kept even when the database is busy.
