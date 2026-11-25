# Scoped business tools

A trusted workspace runner can invoke `conductor business-tool --config /absolute/path/binding.json`.
It creates one typed call from standard input and returns a versioned JSON result. Bindings
and secrets come from the runner. Give a model a tool schema and the arguments shown below.
Keep the command line, config path, credential environment, and mailbox state outside the model's control.

An approved call returns `version`, `tool`, `untrusted_data: true`, and `data`.
Treat subjects, replies, lead fields, and text inside `data` as untrusted business data.
The command exits nonzero with a stable status such as `permission_denied`,
`provider_unavailable`, `stale_record` or `not_found`. It does not log provider request
contents or retain draft text. A consuming runner must keep results in its private workspace.

## Runner setup

The runner supplies a private JSON file with `version: 1` and a `binding` object.
The object contains:

| Fields | Values |
| --- | --- |
| `tenant_id`, `workspace_id`, `connection_id`, `grant_id` | Provisioned UUIDs |
| `provider` | `odoo` or `outlook` |
| `target` | The opaque, configured company or mailbox identifier recorded in the grant |
| `actor_kind`, `actor_id` | `person`, `skill`, or `schedule`, and the verified principal identifier |
| `run_id` | UUID for a skill or schedule, JSON `null` for a person |

Set `AIUDITOR_WORKSPACE_CONNECTION_URL` to the trusted AIuditor HTTPS origin and
`AIUDITOR_WORKSPACE_CONNECTION_KEY` to the dedicated workspace-connection authorizer key.
The command signs each authorization request. Each read uses a fresh decision from the
authority. Drafts check both a read and a draft authorization. A provider or authorization
error stops the operation. CI can use loopback HTTP on `127.0.0.1` or `::1`.

A future MCP or application harness can own the binding, credential delivery and private
output boundary and invoke this same command. The command does not register a public
HTTP endpoint or accept arbitrary API paths, recipient addresses or provider methods.

## Odoo

Set `SUPPORT_API_URL` to the private Odoo owner adapter HTTPS origin and
`SUPPORT_READER_TOKEN` to its scoped reader credential. Odoo 18 stays behind the owning
adapter. Conductor calls only the lead or case HTTP GET routes and sends the authenticated
workspace binding in `X-Support-Workspace`. The adapter maps that workspace to its Odoo
company and enforces company record access. Use the connection grant target configured
for that workspace and company.

| Tool | Arguments |
| --- | --- |
| `odoo.leads.search` | Optional `query` and `limit` |
| `odoo.leads.read` | `id`, a positive decimal string |
| `odoo.cases.search` | Optional `query`, `limit`, and `cursor` from the previous page |
| `odoo.cases.read` | `id`, the opaque case reference |
| `odoo.leads.draft` | `id`, `expected_version`, `action` (`route` or `follow_up`), and `text` |
| `odoo.cases.draft` | `id`, `expected_version`, `action: "reply"`, and `text` |

For example, supply `{"version":1,"tool":"odoo.leads.search","arguments":{"query":"trial","limit":10}}`
to the command. The maximum page size is 50 and the default is 20. Drafts return a
source identifier and expected version, the proposed action and text, and an operation
ID derived from the binding and proposal. A retry with the same source version and content
returns the same ID. Treat it as a suggestion for an operator. Sending and applying updates
require separate authorized product actions.

## Outlook fixture reader

Use only a project-controlled fixture. Set `outlook_fixture` and `outlook_state` to absolute
paths in the binding file, and set `outlook_folders` to the allowed folders. The fixture has
`version: 1`, an opaque `mailbox` matching the grant target, a cursor `generation`, the
RFC 3339 `as_of` time, and `messages`. Each message has an increasing `sequence`, a
`folder`, an opaque `id` and `thread_id`, an RFC 3339 `received_at` time, and optional
`subject` and plain-text `body`. Simulate the provider states with optional fixture
`status` values `consent_required`, `connection_revoked`, `throttled`, `stale_sync`,
and `provider_unavailable`.

| Tool | Arguments |
| --- | --- |
| `outlook.messages.list` | `folder`, optional `limit`, optional `before` from the previous page |
| `outlook.threads.read` | `folder`, `thread_id`, optional `limit` |
| `outlook.messages.sync` | `folder`, `operation_id` UUID, optional `limit` |

The reader uses the last 30 days of fixture data. Lists return subjects and identifiers,
thread reads include bounded plain-text replies, and sync returns metadata only. Sync
persists a cursor and deduplication IDs scoped to a connection and folder in a private
file. Retry an interrupted batch with the same operation ID and limit for up to seven
days. Use a new UUID for the next batch when `more` is true. A changed fixture generation
restarts the history scan and reports `cursor_reset`; previously delivered identifiers
within the history window are skipped.

A future live Graph transport will need a separate authorized rollout. Interactive
shared-folder reads can use [delegated access](https://learn.microsoft.com/en-us/graph/outlook-share-messages-folders)
limited to explicitly selected mailboxes. For unattended reads, use a narrowly scoped
[Exchange Online application RBAC](https://learn.microsoft.com/en-us/exchange/permissions-exo/application-rbac)
role and verify both an allowed and a denied mailbox. Entra permission grants and Exchange
RBAC are additive, so an overlapping tenant-wide mail grant can defeat the mailbox scope.
This command implements no Graph network transport, token flow, attachments, or send actions.
