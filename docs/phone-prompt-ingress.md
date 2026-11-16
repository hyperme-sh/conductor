# Enrolled phone prompt ingress

`conductor serve --phone-plane https://PLANE --phone-interface MESH_INTERFACE
--config CONFIG` enables `/v1/phone/prompt-submit`. It requires network mode,
configured TLS certificates, authenticated AIuditor review, and an explicit
private listen IP currently assigned to the named non-loopback interface.
Provisioning owns that interface and address (Infra #1747); Conductor never
creates peers or guesses a gateway. Socket and inherited-listener modes are
refused for this option. Existing operator routes retain their authentication.

The native phone submits the existing version-1 machine prompt fields plus
`enrollment_id`, its saved client enrollment request UUID. `device_id` is the
phone, not the execution host. The bearer credential stays in the native client.
Plane #175 requires an explicit same-owner grant for the current remote session,
an already delivered owner-declared exact-digest task, and current host/client
authority. Responses bind both caller and execution identities. Missing or
changed authority cancels downstream work; consumed requests are never retried.

The dedicated handler sends only the prompt and bounded routing fields through
Conductor's normal authenticated AIuditor review and destination path. Phone
credentials are not forwarded to providers. Operator proxy credentials cannot
substitute for an enrolled phone identity. All other network endpoints retain
the existing operator credential checks.

This change provides the service ingress, not phone peer provisioning, task
selection/dispatch UI, native request construction, or s3r.ch identity adapters.
No service deployment, signing, version, or release action is included.
