# ExaCC Log Shipping & RCA Ingestion

**Release:** v0.1.0 reference implementation · **Date:** 19 October 2026  
**Scope:** General-purpose, customer-neutral Exadata Cloud@Customer (ExaCC) **log transport** pipeline.  
**Validation:** Builds and passes automated end-to-end tests on Linux with the local filesystem backend. **Not production certified.**

## What this package implements

```text
Customer-authorized ExaCC DB VM                    Central customer-managed environment
+------------------------------+                   +------------------+   +--------------+
| Diagnostic files (read-only) |                   | SeaweedFS / S3   |   | Go shipper   |
| Go exacc-collect             | -- HTTPS PUT --> | object landing   | ->| (validation) |--+
| systemd timer (1 min)        |                   +------------------+   +--------------+  |
+------------------------------+                                                | HTTP(S) POST
                                                                                 v
                                                                 +--------------------------+
                                                                 | Durable sample receiver  |
                                                                 | (NOT an RCA engine)      |
                                                                 +--------------------------+
```

Three Go commands are **implemented**:

- `cmd/exacc-collect`: scheduled, read-only incremental newline-delimited file collector; configurable regex redaction, gzip JSONL batches, signed manifests, durable spool, inode/offset checkpoints, retries and file-system locking.
- `cmd/exacc-shipper`: lists committed manifests, checks HMAC/signature and payload SHA-256, enforces organization allowlist and memory limits, forwards validated events, stores local checkpoint receipts, quarantines repeat failures.
- `cmd/exacc-receiver`: demonstration HTTP(S) service that durably writes one JSON file per acknowledged batch and supports idempotent retry of the same batch ID. **It does not perform root cause analysis.**

Available landing-zone implementations: **filesystem (tested)**, **minimal S3 Signature V4 path-style client (implemented; no SeaweedFS S3 integration test performed)**.

## Quick verification — no ExaCC or OCI access required

Requirements: Go 1.22+, Python 3, curl; supported lab environment Linux. Internet access is **not required** to build, as this module depends only on the Go standard library.

```bash
cd exacc-log-shipper-source
go test ./...
go vet ./...
./scripts/e2e-local.sh
```

The end-to-end test builds all commands, creates a temporary synthetic alert log, uploads to a local filesystem landing zone, forwards events to the receiver, tests redaction, incremental collection, receiver outage, landing-zone outage, spool retry and receiver deduplication. It cleans up its temporary state automatically.

## Interactive local demonstration

Build binaries:

```bash
go build -o ./exacc-collect ./cmd/exacc-collect
go build -o ./exacc-shipper ./cmd/exacc-shipper
go build -o ./exacc-receiver ./cmd/exacc-receiver
```

The examples use `/tmp/exacc-demo` and a receiver bound to `127.0.0.1:18181`:

```bash
mkdir -p /tmp/exacc-demo
printf '2026-10-19 ORA-00257 archiver error\n' > /tmp/exacc-demo/alert.log
# TEST KEYS ONLY — provision strong keys through managed secret files in real deployments.
export EXACC_MANIFEST_HMAC_KEY='DEMO-HMAC-DO-NOT-USE-IN-PRODUCTION-12345'
export EXACC_RECEIVER_TOKEN='DEMO-TOKEN-DO-NOT-USE-IN-PRODUCTION-67890'
./exacc-receiver -config configs/local/receiver.json
```

Then open **another terminal** with the same environment variables:

```bash
./exacc-collect -validate -config configs/local/collector.json
./exacc-collect -config configs/local/collector.json
./exacc-shipper -config configs/local/shipper.json -once
curl -H "Authorization: Bearer $EXACC_RECEIVER_TOKEN" \
  http://127.0.0.1:18181/v1/telemetry/stats
```

Expect `{"committed_batches":1}` after a clean first run. Repeat the collector without appending a newline-terminated record; no additional batch should appear. A continuous central shipper is invoked without `-once`.

## S3 / SeaweedFS handoff

For the S3 profile, copy `configs/s3/*.json` and substitute **real**, approved paths, organization/source IDs and endpoints. Collectors and central shippers need separate scoped AWS-style credentials from the S3 gateway. The S3 client reads `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and optionally `AWS_SESSION_TOKEN`, with HTTPS certificate validation (optional trust CA bundle). The receiver token and manifest HMAC key are read from `EXACC_RECEIVER_TOKEN` and `EXACC_MANIFEST_HMAC_KEY`; never put these in the JSON config or commit them to Git.

For a fresh installation, read `docs/DEPLOYMENT_GUIDE.md`, `docs/SECURITY.md`, and `docs/OPERATIONS.md`. **Do not deploy the current receiver or filesystem state backend for production use without hardening and architecture review.**

## Contents

```text
cmd/                  Collector, shipper, sample receiver entrypoints
internal/             Config, model, S3/filesystem landing, atomic files
configs/              Executable JSON examples for local and S3 profiles
deploy/systemd/       Template units and collector timer
scripts/              Test and build helpers
docs/                 Design, deployment, security, operations, tests, gaps
```

## Design limitations

The project is a tested **reference prototype**, not a complete Exadata management integration. It requires customer permission to read diagnostic files; the actual VM deployment must be approved. It does not access Oracle-managed Exadata cells, scan Oracle SQL, capture AHF, execute RCA inference or remediate issues. Single-instance filesystem checkpoints, bearer tokens, shared HMAC secret and bounded payloads are intentional pilot simplifications. The source tailer handles current-file truncate/replacement but not full rotated-file archive draining; see the gap matrix.

## Licensing and distribution

This package is intended for technical review and handover. It contains no OCI tenancy credentials, sample private keys or real customer identifiers. It does **not** grant rights to third-party products or Oracle software. Obtain organizational source-code ownership and licensing approval before public redistribution.
