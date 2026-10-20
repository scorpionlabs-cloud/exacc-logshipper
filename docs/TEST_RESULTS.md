# Validation Report — v0.1.0

**Execution date:** 19 October 2026  
**Environment:** Linux x86-64 container, Go 1.23.2, Python 3.13, localhost/temporary file landing zone.  
**Command:** `./scripts/e2e-local.sh`  
**Result:** PASS (local backend only)

| Test | Result |
|---|---|
| Go `go test ./...` | PASS |
| Go `go vet ./...` | PASS |
| Compile all three executable commands | PASS |
| Send three synthetic alert events to receiver | PASS |
| Redact a synthetic password from event content | PASS |
| Repeat collector/shipper without new lines (no new batches) | PASS |
| Append one event and collect incrementally | PASS |
| Receiver outage produces shipper failure without committing receipt | PASS |
| Restore receiver and resume queued ingestion | PASS |
| Storage landing-zone outage retains a `.pending.json` batch | PASS |
| Restore storage and upload queued spool safely | PASS |
| Delete shipper receipts and replay all manifests without duplicate batch commit | PASS |
| Persist 6 unique event IDs and parse `ORA-00257` | PASS |
| Signed manifest mutation detected (unit test) | PASS |
| Local landing object key traversal rejected (unit test) | PASS |

**Not tested:** SeaweedFS endpoint, real ExaCC VM, production TLS termination, managed client certificate authentication, Oracle SQL-based diagnostics, log rotation archive draining, production load and resource thresholds, multi-instance HA, PostgreSQL, ServiceNow, AI RCA. Passing local tests does not imply certification for any of these.

Repeat before sharing source to ensure no accidental changes break the test. `scripts/e2e-local.sh` cleans up temporary state unless `KEEP_E2E_FILES=1` is explicitly set.
