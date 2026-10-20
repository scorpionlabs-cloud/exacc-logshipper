# Security & Data Classification Notes

## Intended security boundary

Read-only approved database VM diagnostics -> mutually isolated customer-managed object storage -> central ingestion services. No access is provided to Exadata physical cells, customer application tables, or Oracle cloud tenancy administrative APIs. Product owner must independently confirm Oracle and customer privilege boundaries.

## Implemented prototype controls

- Collector verifies its config paths are absolute and requires explicit source path list.
- Minimal S3 client requires HTTPS and validates the peer's TLS certificate. Optional custom CA bundle; no insecure-skip-verify feature.
- Batch payload is SHA-256 hashed; manifest signed with HMAC SHA-256.
- Central shipper enforces allowed organization IDs and source/manifest agreement.
- Shipper caps compressed data at 32 MiB, expanded payload via config, and individual events at 256 KiB.
- Receiver requires bearer token (>=16 bytes) and refuses plaintext HTTP on non-loopback listeners.
- File data and local state written with restrictive filesystem permissions (`0600`), subject to umask and OS.
- Log data is redacted using *configured* regexes, before storage upload.

## Important gaps — address before production

1. HMAC key is **shared** in a prototype; per-source asymmetric signatures or registered scoped signing keys, rotation and revocation are required for stronger provenance.
2. Regex redaction is not sufficient to guarantee PII/secrets removal; establish vetted policies, structured masking and false-negative tests.
3. Sample receiver is not an authenticated multi-tenant service; replace with managed identity, API gateway, mTLS/OIDC, granular authorization and an auditable database.
4. S3 permissions must be enforced in the deployed SeaweedFS implementation; verify tenancy and prefix/bucket boundaries using negative permission tests.
5. Credentials are read from env; use managed secret injection with rotation and prevent `/proc`, unit output and journal leakage.
6. No audit trail for operators, no rate limiting / quota protection, and no automatic source revocation.
7. No field-level encryption, KMS-backed envelope encryption, integrity chain for forensic export, or immutable/retention-locked bucket management.
8. No threat-model review, penetration testing, fuzzing, disaster recovery certification or full Oracle policy/security assessment.
9. Object manifests list metadata such as organization IDs and node identifiers; classify and protect even if message content is redacted.
10. Receiver stores plaintext JSON on disk. Replace with encrypted, backed-up, transactional storage for production use.
11. No protection for local file source against symlink swaps or adversarial file replacement; deployed collector should adopt hardened descriptor-based allowlist handling.

## Data handling obligations

Telemetry may contain hostnames, account names, SQL snippets, bind variables or credentials depending on diagnostic source and collection method. Conduct a privacy and security classification review. Use synthetic logs for demonstrations and integration tests; never include real customer logs or tenancy secrets in source handoffs.
