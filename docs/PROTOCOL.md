# Transport, Checkpoints and Failure Semantics

## Pipeline

1. Scheduled collector acquires a process lock and retries any existing `.pending.json` spool batches.
2. Collector reads complete newline-terminated records from allowlisted absolute file paths, respecting the configured byte ceiling.
3. Events include stable event IDs derived from org, source, device, inode and byte offset; observed timestamp is collection time, *not parsed Oracle event time*.
4. Collector applies configured regex redaction to the message text, generates JSONL and gzip-compresses it.
5. Collector stages a durable gzip object, then the pending manifest metadata, under `spool_dir`. Both are atomically renamed after fsync.
6. Collector uploads payload first and signed manifest **last**. `*.manifest.json` is the commit marker that the shipper watches.
7. Only after both uploads succeed does the collector atomically commit the file cursor. If a crash occurs earlier, the pending spool is retried with the same batch and object key.
8. Shipper lists manifest keys, verifies HMAC, checks payload key agreement, payload length and SHA-256, enforces organization allowlist, decompresses within its configured maximum, checks event identity and posts batch JSON to receiver.
9. Receiver persists a whole batch atomically and acknowledges it. Re-posting the same batch is acknowledged without a second committed batch.
10. Only after receiver acknowledgement does shipper persist its processed receipt.

## Object layout

```text
v1/org=<organization>/site=<site>/exacc=<system>/cluster=<cluster>/
   node=<node>/source=<source>/YYYY/MM/DD/<batch_id>.jsonl.gz
v1/org=<organization>/site=<site>/exacc=<system>/cluster=<cluster>/
   node=<node>/source=<source>/YYYY/MM/DD/<batch_id>.manifest.json
```

Collector and shipper agree on the exact keys; no customer-specific identifiers are compiled into Go code. Example identifiers in samples are placeholders.

## Manifest example (illustrative; signature elided)

```json
{
  "schema_version":"1.0",
  "batch_id":"<sha256 hash>",
  "identity":{
    "organization_id":"org-example",
    "site_id":"site-a",
    "exacc_id":"exacc-01",
    "vm_cluster_id":"cluster-01",
    "node_id":"node-01"
  },
  "source_id":"db-alert",
  "payload_key":"v1/org=org-example/site=site-a/.../batch.jsonl.gz",
  "payload_sha256":"<sha256 of compressed bytes>",
  "payload_bytes":1234,
  "event_count":42,
  "created_at":"2026-10-19T13:00:00Z",
  "signature":"<hex hmac sha256>"
}
```

The HMAC is over `json.Marshal(manifest)` with `signature` set to an empty string, using `EXACC_MANIFEST_HMAC_KEY`. It provides sender/shared-key integrity for the pilot, *not* per-host identity proof. Production should assign registered per-source signing keys and use stronger rotation and revocation controls.

## Acknowledgement contract

`POST /v1/telemetry/events:batch` with JSON `{ "batch_id":"...", "events":[...] }`. Use bearer token via `EXACC_RECEIVER_TOKEN`. Response `202` on first commit and `200` on identical replay. `409` on same ID with different body. The demo receiver accepts only a 64-char lower-hex batch ID and atomically writes `{data_dir}/batches/{batch_id}.json`.

## Delivery guarantees

- **At-least-once across collector, storage and receiver.** Replays can occur after crashes and partial network failures.
- The demo receiver prevents repeated commits of an identical **batch ID**. It does not globally index duplicate **event IDs** across two different batches. A consumer must be able to deduplicate events using `event_id` in a production event store.
- Cursor checkpoint is advanced only after successfully publishing payload and manifest.
- A shipper receipt is recorded only after receiver's durable acknowledgment.
- **No exactly-once delivery claim**, guaranteed end-to-end latency or zero-loss guarantee under spool exhaustion/rotation.

## Known input constraints

One complete log record per newline; no collection of unterminated final lines until newline appears. Log lines larger than the batch maximum stop that source with an error. Current active files can be read after truncation or inode replacement, but this prototype does not drain old rotated files. File-system state uses a single collector process lock and is not distributed.

## S3 path-style implementation

The included minimal S3 client supports HTTPS path-style S3 `PutObject`, `GetObject`, `ListObjectsV2` with paging and AWS SigV4. It uses standard environment credentials and a configurable CA certificate. It has **not been integration tested against a specific SeaweedFS release**; run interoperability tests, certificate validation, IAM scope tests, and failure tests before use.
