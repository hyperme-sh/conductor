# Permission preview client

`conductor permission-preview --plane https://PLANE --authority
https://AIUDITOR/api/v1/aiuditor/access/preview` reads one bounded JSON object from
stdin: `credential` contains an existing enrolled machine credential and
`preview` contains the version-1 request documented by AIuditor. The host bridge
supplies the credential privately; Visor never needs to obtain or copy it.

The client independently checks current owner/device/assignment/session context
with Plane, then asks AIuditor for the policy preview. It supplies the first
context expiry as `context_expires_at`; AIuditor may shorten but cannot extend
that deadline. Replies must match both the authenticated identity and requested
action, project and request ID. Non-canonical input that requires target rewriting
is refused; callers should submit an explicit normalized target.

Advisory and prohibited responses are successful preview results, not transport
errors. Only permitted results may be eligible for learning, and every result
must have `preview_only: true`. No execution or grant mutation is exposed.
Credentials are passed in authenticated HTTPS headers, never in policy bodies,
argv, output or error messages. Redirects and untrusted endpoints are refused.
