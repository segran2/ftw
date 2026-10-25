---
"ftw": patch
---

Stopping the OCPP server now also closes a charger connection that was still opening. Before, that connection could stay open on the stopped server. Core stops the server only when it shuts down, so chargers on a running box are not affected.
