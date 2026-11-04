# Security policy

## Supported versions

Fugato is preparing its first public release. Security fixes currently target
`main`. After releases are published, fixes will target `main` and the latest
published release; older versions may need to upgrade.

## Reporting a vulnerability

Do not disclose vulnerabilities, credentials, or customer data in public issues,
pull requests, or discussions.

The intended private reporting route is GitHub's
[Report a vulnerability form](https://github.com/fugatolabs/fugato/security/advisories/new).
It is not active while this repository remains private. Maintainers must enable
private vulnerability reporting and verify that form before the public launch.
If the form is unavailable, do not post sensitive details publicly; no alternate
private contact is currently designated here.

Once private reporting is enabled, include the affected version or commit,
operating system, expected and actual behavior, impact, and a minimal reproduction
using synthetic data. Redact tokens, environment files, personal paths, and
customer content. An exposed credential should also be revoked or rotated with
its provider; removing it from Git does not invalidate it.

Maintainers will use the private advisory to discuss reproduction, remediation,
and coordinated disclosure. This project does not currently promise a response
time or offer a bug bounty.
