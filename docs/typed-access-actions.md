# Typed action verification

`permission-action-review` uses the existing private credential envelope and
configured HTTPS owners. It obtains active Plane context and calls AIuditor's
`/api/v1/aiuditor/access/action-review`, checking the returned exact action,
identity, project, policy revision, limits and expiry. Preview/read responses
cannot authorize a typed action. The CLI never executes an action.

The action includes `arguments_json` and its exact SHA-256. HTTPS binds method,
resolved addresses and body digest/size; writes bind content and existing object
and parent identities; service restart binds a named unit, non-root UID,
executable identity and controller binding. Unknown fields, duplicate argument
keys, path escape, changed argument bytes and obvious private destinations are
refused before contacting owners. AIuditor independently applies its public-IP
and operation-policy constraints. No redirects or credential destination changes
are accepted.

Supports #69 and AIuditor #99. Host and Visor must enforce reviewed destinations,
OS identities and durable reservations at execution; this adapter does not grant
blanket network, write or shell access. Existing read and preview behavior remains
compatible. No version or publication changes.

`permission-action-admit --config <trusted-dispatch-config>` repeats the current
owner review and reserves the request ID in the existing durable dispatch
journal before returning `reserved: true`. A consumed ID is refused after a
process restart as well. Admission does not execute the action. The private
Host bridge supplies the trusted configuration; callers cannot choose a new
journal. A lost response is an unknown outcome and must not be automatically
retried with a different ID.
