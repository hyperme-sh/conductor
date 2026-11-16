# Machine prompt ingress, version 1

`conductor serve --socket --machine-plane https://PLANE --config CONFIG`
enables `POST /v1/machine/prompt-submit` on the existing protected local socket.
The ordinary public listener is not enabled by this option. HTTP AIuditor mode
with the dedicated review credential/tenant is required; stub mode is refused.
The tenant in each submission must match the configured review tenant.

The caller uses the existing enrolled `hm_msi_` credential, obtained through its
host identity owner. Renter keys, bootstrap secrets and caller JWTs are not
substitutes. The HTTPS Plane origin is operator configuration, not request input.
No redirects are followed and public failures contain fixed text.

Submission fields are `version: 1`, `tenant_id`, `device_id`, `bot_id`,
`assignment_id`, `session_id`, `task_id`, `operation_id`, `class`, `destination`,
`prompt` and `max_output_tokens`. UUIDs are canonical and nonzero. The destination
is the requested model; existing routing and AIuditor constraints remain in
force. Prompt text is limited to 64 KiB and output to 1–32768 tokens. The owner
must dispatch a prompt task on Plane with payload
`{"version":1,"request_sha256":"SHA256_OF_EXACT_SUBMISSION_BYTES"}`. Serialize
once and retain those bytes: reformatting JSON changes the digest.

Plane first binds the bot to its owner's completed host registration. Its
`/api/v1/hypermesh/bots/{bot}/machine/operation` endpoint then checks current
machine identity, assignment, session, delivered task, connection, heartbeat,
local consent and the exact digest before durably consuming the operation.
Conductor passes only the prompt, class, model and bounded output to its normal
proxy; machine credentials never reach the provider or note service. The normal
Conductor → AIuditor → destination path remains responsible for policy/routing.

Admission expires within ten seconds and is rechecked once per second inside a
five-minute request bound. An unavailable check, changed identity, pause,
revocation, assignment expiry or client disconnect cancels downstream context.
Cancellation cannot undo an already-dispatched effect. Plane records that outcome
as unknown; consumers must reconcile rather than resubmit. Duplicate begins,
including after process restart or a lost response, never dispatch a second time.

The shared owner client also supports read-only `/machine/context` for permission
preview consumers. This returns authenticated owner/organization/device/session
facts, not execution permission. Visor #264 adds the separate policy preview.

Unit contracts use isolated HTTPS owner doubles and cover response binding,
credential isolation, duplicate dispatch and cancellation. No provider activation,
package-version change, release or production deployment is part of this change.
