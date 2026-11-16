# Private machine client helpers

The root-managed Host bridge uses `machine-context --plane HTTPS_ORIGIN` to
verify its configured assignment and `machine-prompt --socket PRIVATE_SOCKET`
to submit an owner-admitted turn. The first command reads a credential and binding
from stdin. The second reads a credential and `submission_base64`, preserving the
exact bytes whose digest the owner committed to Plane. Neither puts credentials
or prompts in arguments or diagnostics.

The host selects both destinations from its protected configuration, never from
Visor or editor input. The socket client checks private path ownership and peer
identity, uses a fixed machine-ingress path and never follows redirects or retries.
The existing ingress still verifies Plane admission and routes through AIuditor.
Closing the client cancels its upstream request; unknown outcomes are not replayed.

These helpers are the Conductor adapter for Visor #219 and hypermesh-vscode #9.
They do not create owner tasks or grant execution independently.
