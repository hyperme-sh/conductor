# Stored recipe drafts

`POST /v1/recipes/draft` owns the current saved draft for a tenant and organization.
Saving validates with the same recipe loader as active configuration, but never
changes the configured file, active limits, model endpoints or a running task.
The stored revision is an opaque UUID for conditional updates, not an active
recipe hash. There is no automatic retry, history API or activation operation.

The route is disabled unless `CONDUCTOR_RECIPE_STORE_ENABLED=true`.
`CONDUCTOR_RECIPE_STORE_DIR` must name an existing absolute private directory
(mode 0700, no symlink components) on a filesystem supporting SQLite locking.
Conductor stores `recipes.sqlite` there with private file permissions. Keep this
owner directory on persistent local storage and back it up with the instance.
The route refuses storage that cannot satisfy these protections.

`CONDUCTOR_RECIPE_BINDINGS` is a JSON array of at most 64 explicit assignments:
`tenant_id`, `organization_id`, `subject`, and `token_env`. Scope IDs are lowercase
UUID strings. `token_env` names a `CONDUCTOR_RECIPE_TOKEN_*` secret (32–4096 printable
ASCII bytes). Each assignment needs a unique dedicated secret; duplicate scope
assignments and reused secrets fail closed. General proxy/read/reload credentials
never authorize this route. No bindings or secrets are provisioned by the feature.

The trusted Portal server must recheck current User authority before every call.
It sends `X-Conductor-Recipe-Token`, `Content-Type: application/json`, and JSON
with the assigned scope/subject and `action: "read"` or `action: "save"`.
Save also requires `expected_revision` (null for create, otherwise the last read
UUID) and `recipe` (a string of at most 65536 UTF-8 bytes). Scope is bound to the
credential; caller scope fields alone grant nothing. Assigned subjects in one
organization share that organization's draft. Other tenants/organizations do not.

Responses use `hypermesh.conductor.recipe-draft.v1`, `outcome`, `revision` and
`recipe`. A read returns `loaded`, with both values null when absent. Save returns
`saved` and the new revision without echoing the recipe. A stale write returns
409 `conflict` without disclosing another draft; explicitly read before deciding
whether to replace local edits. Invalid drafts return 422 `rejected` plus a fixed
validation `field`. Unavailable responses contain no parser/configuration text.
Transport failure after submission is ambiguous: read before another save.

SQLite transactions serialize cross-process comparisons and updates. A save
validates against the active configuration snapshot under the reload lock, but
that configuration can change afterward; stored drafts must be revalidated before
future use. No remaining allowance or actual usage is implied by saving.

Unit/contract tests cover persistence across owner recreation, conflicting writers,
foreign scope/credential refusal, disabled/private storage, validation rejection
and unchanged active limits. Composed browser and service journeys remain in
scheduled CI; this feature does not provision or activate a shared fixture.
