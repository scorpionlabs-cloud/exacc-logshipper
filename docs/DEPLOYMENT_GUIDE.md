# Deployment & Configuration Guide (Linux, Reference Prototype)

## Before installation

1. Confirm ExaCC VM host administration permits custom read-only collection scripts. Obtain approvals from both the customer and Oracle support policy owners where applicable.
2. Identify approved customer-accessible database VMs. This package does **not** require or authorize access to Oracle-managed Exadata physical cells, storage servers or networking.
3. Identify log paths using `adrci` as an Oracle-authorized account (`show base`, `show homes`). Grant narrow **read-only** rights to collector principal; do not change Oracle software ownership or directory permissions casually.
4. Obtain S3-compatible SeaweedFS gateway hostname, trusted HTTPS CA, bucket, prefix-scoped upload and read credentials, and independent receiver endpoint.
5. Agree on redaction policy, source log retention, rate/spool limits, network route, alarms and customer privacy classification.
6. Decide operating system paths; systemd templates and sample JSON configs must be adjusted to match them before installation.

## 1. Build from source on a trusted machine

```bash
./scripts/build-linux.sh
```

Requires Go 1.22+ and no dependency downloads. The script creates `dist/linux-amd64/` and `dist/linux-arm64/`. Deploy the binary that matches each approved VM CPU architecture. Treat build provenance, signing and anti-malware scanning as separate release activities.

## 2. Prepare collectors on each approved ExaCC VM

Example commands (run only after local OS/Oracle operational approval):

```bash
sudo useradd --system --home-dir /var/lib/exacc-insight --shell /sbin/nologin exaccinsight
sudo install -d -m 0750 /opt/exacc-insight/bin /etc/exacc-insight
sudo install -d -o exaccinsight -g exaccinsight -m 0700 \
  /var/lib/exacc-insight/state /var/lib/exacc-insight/spool
```

Place `exacc-collect` in `/opt/exacc-insight/bin/` and create `/etc/exacc-insight/collector.json` using `configs/s3/collector.json` as a starting point. Only add absolute, allowlisted log paths. Ensure the collector user can read *only* those files and write *only* its state/spool paths.

Example environment file `/etc/exacc-insight/collector.env` (contents are placeholders, not deployable secrets):

```text
AWS_ACCESS_KEY_ID=<scoped-collector-access-key>
AWS_SECRET_ACCESS_KEY=<scoped-collector-secret>
EXACC_MANIFEST_HMAC_KEY=<long-random-manifest-key>
```

Secure the environment file (`root:root`, `0600`) and consider OS credential-management facilities instead of long-lived plaintext files. `EXACC_MANIFEST_HMAC_KEY` must match the central shipper's configured key in this pilot.

Run configuration-only validation:

```bash
sudo -u exaccinsight /opt/exacc-insight/bin/exacc-collect \
  -validate -config /etc/exacc-insight/collector.json
```

Validation checks config schema, redaction expressions and identity formats; it does **not** test filesystem permissions, S3 access or receiver connectivity. Run a non-production synthetic test through the full collector command before scheduling.

## 3. Install scheduled collector execution

Copy `deploy/systemd/exacc-collect.service` and `exacc-collect.timer` into `/etc/systemd/system/`; review sandbox paths first. If SELinux is enabled, coordinate policy changes with OS administrators. Enable:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now exacc-collect.timer
sudo systemctl list-timers exacc-collect.timer
sudo journalctl -u exacc-collect.service -n 50 --no-pager
```

The collector internally locks its state directory to prevent overlapping runs. The timer is deliberately once per minute, not a persistent ExaCC agent. Reconfigure the schedule only after load and capacity measurements.

## 4. Prepare SeaweedFS / S3

Provision S3 bucket `exacc-telemetry`. Set an authenticated HTTPS gateway and a scoped collection identity with PUT allowed only to its designated prefix, preferably no DELETE. Create a separate shipper identity with LIST/GET/HEAD-only permissions and no DELETE. Confirm actual SeaweedFS version policies and prefix authorization behavior, using separate buckets if required to enforce isolation.

From an authorized client using AWS CLI and appropriate profile:

```bash
aws --profile exacc-collector \
  --endpoint-url https://s3.internal.example \
  s3api list-buckets
```

The above LIST buckets call may correctly be denied for minimally scoped collector credentials. Instead, validate only the required PUT using an approved synthetic object key, and verify the object with shipper's read-only identity. Never disable TLS certificate checks to work around an untrusted CA.

## 5. Configure central shipper

On a **separate customer-managed server**, install `exacc-shipper`, create `/etc/exacc-shipper/shipper.json` based on `configs/s3/shipper.json`, and provision `/var/lib/exacc-shipper/state` with mode `0700`. Example `/etc/exacc-shipper/shipper.env` variables:

```text
AWS_ACCESS_KEY_ID=<scoped-shipper-read-key>
AWS_SECRET_ACCESS_KEY=<scoped-shipper-read-secret>
EXACC_MANIFEST_HMAC_KEY=<same-pilot-manifest-key-as-collector>
EXACC_RECEIVER_TOKEN=<long-random-receiver-bearer-token>
```

The receiver URL must use HTTPS for non-loopback addresses. Adjust organization allowlist. Copy and enable `deploy/systemd/exacc-shipper.service`.

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now exacc-shipper.service
sudo journalctl -u exacc-shipper.service -f
```

**Production implementation must replace local shipper state with a transactional database and leased work queue** before using multiple shipper replicas.

## 6. Sample receiver

`exacc-receiver` is a durable integration-test endpoint, **not** the RCA engine. For a lab, use loopback HTTP. For any non-loopback listener, both `tls_cert_file` and `tls_key_file` are mandatory in the receiver JSON. Install its optional systemd unit only in isolated test environments. The sample receiver uses per-batch JSON files rather than PostgreSQL, has no multi-tenant RBAC, and must not be exposed publicly.

The receiving API contract is described in `PROTOCOL.md`. A production RCA ingestion API should implement the same durable acknowledgement and idempotent batch-ID semantics (plus event-ID deduplication) with an appropriately governed datastore.

## 7. Acceptance checks

- Run `./scripts/e2e-local.sh` before deploying anything on ExaCC.
- Verify authorized readable log files and valid source IDs, no secrets in logs/objects, correct TLS CA and S3 authentication.
- Confirm object has both a `.jsonl.gz` payload and a `.manifest.json` commit marker.
- Confirm the shipper marks a manifest as processed only after receiver acknowledgement.
- Confirm receiver downtime does not advance shipper state and object storage downtime does not advance collector's cursor.
- Confirm retention, file rotation handling, spool overflow behavior and source log purging policy with the DBA team.

## 8. Deployment cautions

The Word technical design includes YAML future-state examples; the binaries in this ZIP accept **JSON** flags (`-config`, `-once`, `-validate`) as shown here. The source does not perform installation, SQL telemetry extraction or automatic Oracle privilege configuration. The minimal S3 client needs vendor-specific compatibility and resilience testing before deployment.
