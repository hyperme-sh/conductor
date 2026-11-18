Agent advice is a pure, bounded suggestion. It does not control a worker.

Defaults: observations expire after 300 seconds; three repeats of an approach,
three corrections/stale assumptions, or two scope-drift observations suggest a
strategy change. At 90% context use, preserve progress and compact rather than
retire. A temporary tool failure suggests checking availability. Completion may
suggest retirement; stale/missing metrics never justify replacement.

The optional `thresholds` member of an advice request overrides these defaults
within documented validation bounds. Current task outcomes come from Plane;
owner-entered observations carry their timestamp and missing values stay null.
No metric is an access-policy decision. Handoffs include goal, constraints,
completed count, files/links/checks, failed approaches, blockers and next step.
