{{/*
What this chart refuses to render.

Every check here is a misconfiguration that Kubernetes would ACCEPT and that
would then be wrong at runtime — the failure mode this project treats as the
expensive one. A rejected install costs a minute; a scheduler quietly
materialising duplicate runs costs a day of somebody's data.

They live in one file because a validation buried next to the resource it
guards is a validation nobody knows exists.
*/}}
{{- define "brevis.validate" -}}

{{- if and (not .Values.database.url) (not .Values.database.existingSecret) -}}
{{- fail "\n\nbrevis: database.url or database.existingSecret is required.\n\n  --set database.url=\"postgres://user:pw@host/brevis?sslmode=require\"\n\nThe engine has no embedded database and `serve` never creates the schema:\nmigrations run as their own Job, which this chart installs as a pre-install\nhook.\n" -}}
{{- end -}}

{{- if and .Values.database.url .Values.database.existingSecret -}}
{{- fail "\n\nbrevis: database.url and database.existingSecret are both set, and only one can win.\n\nPick one. Two sources for the same connection string is how a release ends up\npointing at the wrong database after somebody edits the Secret and nothing\nchanges.\n" -}}
{{- end -}}

{{- if ne .Values.env "local" -}}
  {{- if not .Values.auth.existingSecret -}}
    {{- if or (not .Values.auth.user) (not .Values.auth.passwordHash) (not .Values.auth.secret) -}}
{{- fail "\n\nbrevis: env is not `local`, so a credential is required.\n\n  --set auth.user=admin \\\n  --set auth.passwordHash=\"$(brevis hash)\" \\\n  --set auth.secret=\"$(openssl rand -base64 48)\"\n\nAll three together, or auth.existingSecret with keys user/passwordHash/secret.\n\nThe engine itself refuses to boot without them, so a chart that rendered this\nwould hand you a CrashLoopBackOff instead of an error. The interface triggers\npipelines: a POST to /workflows/<slug>/trigger runs a `dbt build` that writes\nto the warehouse.\n" -}}
    {{- end -}}
    {{- if lt (len .Values.auth.secret) 32 -}}
{{- fail (printf "\n\nbrevis: auth.secret has %d bytes and needs at least 32.\n\n  --set auth.secret=\"$(openssl rand -base64 48)\"\n\nIt signs the session cookie. The engine enforces the same minimum at boot.\n" (len .Values.auth.secret)) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- if .Values.api.ingress.enabled -}}
  {{- if not .Values.api.ingress.host -}}
{{- fail "\n\nbrevis: api.ingress.enabled is true and api.ingress.host is empty.\n\nAn Ingress with no host matches every request that reaches the controller,\nwhich in a shared cluster means this release answering for somebody else's\nhostname.\n" -}}
  {{- end -}}
  {{- if and .Values.api.ingress.tls.enabled (not .Values.api.ingress.tls.secretName) -}}
{{- fail "\n\nbrevis: api.ingress.tls.enabled is true and api.ingress.tls.secretName is empty.\n\nWith no secretName the controller falls back to its default certificate, and\nthe interface answers on https with a certificate for another name -- which\nbrowsers reject and operators read as an outage.\n" -}}
  {{- end -}}
{{- end -}}

{{/*
The one value the chart refuses to honour rather than merely validate.

The schema accepts the key only so this message can exist: "Additional property
replicas is not allowed" would say what happened and not what it costs.
*/}}
{{- if hasKey .Values.scheduler "replicas" -}}
{{- fail (printf "\n\nbrevis: scheduler.replicas is set to %v, and this chart runs exactly one.\n\nTwo replicas would not hand out the same queue item -- the dispatcher claims\nwith FOR UPDATE SKIP LOCKED. They WOULD both materialise the same slots, and\nwhat protects against that is an idempotency key, not a lock: the second\nreplica creates the same scheduled run again, and a `dbt build` runs twice over\nthe same window.\n\nNothing fails when that happens. It shows up as duplicated rows in a report\nlater.\n\nUntil there is leader election, one replica is what keeps the behaviour\nobvious. To scale reads, raise api.replicas -- the API is stateless. To run\nmore work at once, raise scheduler.concurrency and scheduler.maxPods.\n" .Values.scheduler.replicas) -}}
{{- end -}}

{{- if lt (int .Values.scheduler.maxPods) 1 -}}
{{- fail "\n\nbrevis: scheduler.maxPods has to be at least 1.\n\nZero is not `unlimited`: it is a scheduler that claims work and never creates\na pod for it, so every run sits in the queue looking claimed.\n" -}}
{{- end -}}

{{- if lt (int .Values.scheduler.concurrency) 1 -}}
{{- fail "\n\nbrevis: scheduler.concurrency has to be at least 1.\n" -}}
{{- end -}}

{{- if and .Values.slack.webhook .Values.slack.existingSecret -}}
{{- fail "\n\nbrevis: slack.webhook and slack.existingSecret are both set. Pick one.\n" -}}
{{- end -}}

{{- if and .Values.alerts.enabled (not (or .Values.slack.webhook .Values.slack.existingSecret)) -}}
{{- fail "\n\nbrevis: alerts.enabled is true with no Slack webhook configured.\n\n  --set slack.webhook=\"https://hooks.slack.com/services/...\"\n\nThe alert pod would start, drain the outbox and deliver nowhere -- and a\ndelivered-nowhere alert is worse than none, because the outbox empties and the\nfailure looks announced.\n" -}}
{{- end -}}

{{- end -}}
