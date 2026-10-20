# Handover Checklist

- [x] Customer-neutral architecture and config examples (no PSA-specific identifiers)
- [x] Source code for read-only collector, Go shipper and sample receiver
- [x] Local and S3 configuration examples
- [x] systemd template collector service and timer; shipper and receiver services
- [x] Offline-compile instructions and local end-to-end test
- [x] Reference architecture DOCX copied into the bundle
- [x] README, protocol, deployment, operations, security and implementation gap documentation
- [x] Secrets not embedded in release configuration files
- [ ] Validate with customer's Oracle/ExaCC operating procedures and policies
- [ ] Exercise the S3 client against a named SeaweedFS version with actual TLS and ACL policy
- [ ] Implement and validate production SQL/ADR source plugins and rotation-archive draining
- [ ] Deploy production database-backed ingestion API and RCA engine
- [ ] Threat model, penetration test and production operating sign-off

The checkboxes marked incomplete are genuine project work, not omissions hidden by this handoff.
