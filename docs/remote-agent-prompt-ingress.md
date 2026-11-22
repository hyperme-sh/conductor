# Enrolled remote-agent prompt ingress

`conductor serve --remote-agent-plane https://PLANE --remote-agent-interface MESH_INTERFACE --config CONFIG`
enables `POST /v1/remote-agent/prompt-submit` on the explicit private TLS
listener. The service requires authenticated AIuditor review and an address
assigned to the provisioned mesh interface. Both flags are opt-in.

The request uses the same version-1 submission fields as phone ingress. Set
`device_id` to the enrolled agent client and `enrollment_id` to its current
enrollment. Keep the dedicated `hm_rag_` bearer credential in the client secret
store. Conductor calls Plane at `/api/v1/hypermesh/bots/{bot}/remote-agent/operation`
to verify the grant, verify the exact request bytes and consume the current
Visor-delivered operation. Plane must implement this authority before the route
can serve agent requests.

Conductor sends only the prompt and bounded routing fields through AIuditor and
the configured model route. It rechecks the grant until dispatch ends and
cancels work when the check fails. Reconcile an uncertain response through Plane
before the owner admits new work.

## Shared model capacity

A dedicated remote-agent runtime with an active owner model lease sets
`--remote-agent-model-lease LEASE_UUID` and `--remote-agent-model-gateway HTTPS_ORIGIN`.
Provide the matching renter credential in `CONDUCTOR_REMOTE_AGENT_RENTER_KEY`
through the runtime's private configuration. Prompt tiers must use the declared
gateway at `HTTPS_ORIGIN/v1` with gateway enforcement and passthrough authentication.

Plane must confirm the same lease when starting and renewing an operation.
Conductor uses its configured renter credential and the admitted lease for
gateway dispatch. A request that loses or changes the lease cannot reach the gateway.
