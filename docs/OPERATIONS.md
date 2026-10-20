# Operations & Incident Runbooks

## Health checks

```bash
systemctl status exacc-collect.timer
journalctl -u exacc-collect.service -n 100 --no-pager
systemctl status exacc-shipper.service
journalctl -u exacc-shipper.service -n 100 --no-pager
curl -f https://<approved-receiver-host>/healthz
```

The receiver's `/v1/telemetry/stats` endpoint requires the bearer token. The health endpoint is deliberately minimal and does not imply receiver datastore capacity or downstream RCA status.

## Failed collector upload

**Symptoms:** Error `payload upload`, spool `.pending.json` files, unchanged cursor.

1. Verify the ExaCC VM can reach the S3 gateway via approved private route and TLS chain.
2. Verify write-scoped collector S3 credentials and bucket/prefix permissions; do not use shipper read-only credentials.
3. Check `/var/lib/exacc-insight/spool/` size against configured `spool_max_bytes`; do not delete valid pending batches.
4. Correct transport/permission failure. Run `exacc-collect -config ...`; existing pending batches upload before new log reading.
5. Validate there are no pending manifests and that current-file cursor advanced. If spool was exhausted, investigate source retention gap explicitly.

**Do not** manually advance offsets or remove queued batches without documenting possible data loss.

## Backlogged central shipper

**Symptoms:** Manifest object count grows; shipper shows errors or quarantine entries.

1. Review shipper journals for signature errors, checksum mismatches, unauthorized organization IDs or receiver connectivity failures.
2. Confirm central server S3 GET/LIST permissions and receiver TLS trust chain.
3. Confirm receiver is durable and returns 2xx only after batch commit.
4. Examine `$state_dir/failed/*.json` and `$state_dir/quarantined/*.json` for problematic manifests.
5. Verify source payload SHA-256 and manifest signature using trusted offline validation before reconsidering quarantine.
6. For authorized replay, remove a corresponding `quarantined` state record **after** correcting the cause; record operator identity, change request and manifest key. The service retries on its next poll.

The single-instance state directory is not safe for active/active shared use.

## Receiver outage

Receiver failure leaves raw object payloads intact. The shipper does not commit processed receipts. Once the receiver is restored and returns durable 2xx, polling resumes ingestion. The demonstrator quarantines after five consecutive failures; manual review is then required. This simple rule should be replaced by separate transient and permanent error policies in production.

## Source log rotation and truncation

If active log file inode changes or file shrinks, collector starts from offset zero on the new active file. This **does not** recover records in the rotated archive between last collection and rotation. Recommend a separate approved rotation-aware collector design (file pattern, inode registry, retention window, and slow reader handling) before deployment to production log sources.

## Forensic replay

Object storage is the recovery source of truth. The shipper's processed receipts use `SHA256(manifest_key).json` filenames under `$state_dir/processed`. Under a change-controlled recovery procedure, removing a receipt causes replay, while the sample receiver returns a duplicate acknowledgement if the same batch is already persisted. Do not remove batch data from the receiver unless an actual authorized reset is required.

## Operational metrics to add before production

Collector: source lag, last source offset/time, accepted byte rate, oldest pending batch age, spool capacity, redaction failures. Shipper: manifests queued, failed/quarantined, object checksum failures, receiver latency, records per batch, dead letter queue. Receiver: durable ingest latency, dedup hits, event count, disk pressure, API errors. Implement metrics export and alerts; this pilot currently emits structured-ish text logs only.
