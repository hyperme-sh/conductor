# Matching verification for a local read

`conductor permission-read-review --plane HTTPS_ORIGIN --authority HTTPS_ENDPOINT`
accepts the existing private preview envelope on stdin. The configured authority
must end in `/api/v1/aiuditor/access/read-review`. Credentials stay off argv and
are sent only to the configured HTTPS owners without redirects or retries.

Conductor independently fetches Plane's active assignment context and forwards
its expiry bound to AIuditor. It verifies the returned identity, request ID,
canonical project/file, argument digest, limits, current policy revision and
expiry. An optional `expected_policy_revision` prevents a remembered rule from
silently surviving a policy change. Preview responses cannot authorize a read.

Only explicit canonical file reads within a non-root project are accepted. The
result is matching owner verification, not an OS read or persistent grant. Visor
must bind the actual opened file, check OS access and cancellation, enforce the
limits, and persist only scoped metadata after successful reads. Ordinary
advisories remain permitted; prohibitions and extreme policy remain refused.

Supports #68 and depends on Plane #177 and AIuditor #95. TLS owner substitutes in
unit/contract checks cover mismatched identity/action/revision, expired context,
paused assignment and redirected responses. No version or publication changes.
