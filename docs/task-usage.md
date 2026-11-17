# Task usage query

`POST /v1/tasks/usage` exposes a read-only snapshot of one authenticated machine
task's local token ledger. It is disabled unless
`CONDUCTOR_TASK_USAGE_ENABLED=true`. This change supplies the Conductor
prerequisite for Portal #41; it does not configure or enable a Portal consumer.

Machine admission carries the current Plane tenant, organization, canonical
owner ID and task ID through an internal context into token admission. The
first reservation binds that scope permanently. Another scope, an unscoped
proxy request, a changed limit, or a legacy unbound task cannot reuse it.
Browser headers cannot supply this internal authority. Tasks without configured
task accounting do not acquire a readable balance.

Query authorization is separate from recipe, preview and proxy authorization.
`CONDUCTOR_TASK_USAGE_BINDINGS` is a JSON array of at most 64 objects with
`tenant_id`, `organization_id`, `owner_id`, and `token_env`. IDs are canonical
nonzero UUIDs. Each `token_env` names a distinct
`CONDUCTOR_TASK_USAGE_TOKEN_...` environment variable containing a unique
32–4096 character printable ASCII secret. Duplicate owners or secrets refuse
the entire configuration. No bindings or credentials are installed by this PR.
The intended future Portal backend must resolve the current canonical owner
from its authenticated authority; an OAuth subject or browser-supplied ID is
not a substitute. These dedicated server credentials must never reach browsers.

Supply that secret in `X-Conductor-Task-Usage-Token`, JSON content type, and a
body with exactly `tenant_id`, `organization_id`, `owner_id`, `task_id`. The
owner must match both the credential binding and persisted task binding.
Bodies are limited to 4096 bytes. Responses use `Cache-Control: private,
no-store`; refusal returns a fixed `unavailable` result without disclosing
whether another owner's task exists. No prompts, model responses, credentials,
or detailed attempt history are returned.

A successful response has `schema: hypermesh.conductor.task-usage.v1`,
`outcome: available`, and `usage` containing:

- `measured_input_tokens`, `measured_output_tokens`, `measured_attempts`: only
  complete measured provider counts that have been durably reconciled.
- `held_input_tokens`, `held_output_tokens`, `held_attempts`: reservations still
  held, including unknown outcomes. These are not actual usage. Held input is
  null for output-only accounting with unresolved attempts, because that mode
  does not reserve input.
- `output_limit`, `total_limit`, `remaining_output_tokens`,
  `remaining_total_tokens`: the admitted limits and conservative remaining
  allowances after both measured and held charges. An absent limit and its
  remaining allowance are null. Exhausted/overrun balances are zero.

The snapshot is current only for its read transaction, not a promise that a
future request will fit. Missing, legacy, unbound or corrupt ledgers are
unavailable. Reads open SQLite in read-only mode and never create a file,
migrate schema, reserve tokens, dispatch a model or adopt old rows. Unknown
outcomes retain their reservations. Query values are bounded for exact browser
integer representation; oversized aggregates refuse instead of rounding.
