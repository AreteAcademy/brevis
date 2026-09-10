{{- define "brevis.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "brevis.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "brevis.labels" -}}
app.kubernetes.io/name: {{ include "brevis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* The API's image: distroless, because it executes nothing. */}}
{{- define "brevis.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/*
The worker image: the same binary on alpine.

The scheduler runs no pipeline -- the pod it creates does -- but `brevis run`
in local mode and any `run:` executed in-process need a shell, and the
distroless image has none.
*/}}
{{- define "brevis.workerImage" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}-worker
{{- end -}}

{{/* Where the database URL comes from, whichever way it was given. */}}
{{- define "brevis.databaseEnv" -}}
- name: BREVIS_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret | default (printf "%s-db" (include "brevis.fullname" .)) }}
      key: {{ .Values.database.existingSecretKey }}
{{- end -}}

{{/* The credential, from the chart's Secret or the operator's own. */}}
{{- define "brevis.authEnv" -}}
{{- $secret := .Values.auth.existingSecret | default (printf "%s-auth" (include "brevis.fullname" .)) }}
{{- if or (ne .Values.env "local") .Values.auth.existingSecret .Values.auth.user }}
- name: BREVIS_AUTH_USER
  valueFrom: {secretKeyRef: {name: {{ $secret }}, key: user}}
- name: BREVIS_AUTH_PASSWORD_HASH
  valueFrom: {secretKeyRef: {name: {{ $secret }}, key: passwordHash}}
- name: BREVIS_AUTH_SECRET
  valueFrom: {secretKeyRef: {name: {{ $secret }}, key: secret}}
{{- end -}}
{{- end -}}

{{/* Common to every process: nothing here is per-role. */}}
{{- define "brevis.commonEnv" -}}
- name: BREVIS_ENV
  value: {{ .Values.env | quote }}
- name: BREVIS_LOG_LEVEL
  value: {{ .Values.logLevel | quote }}
{{- if .Values.brand.enabled }}
- name: BREVIS_BRAND_FILE
  value: /etc/brevis/brand.yaml
{{- end }}
{{- if or .Values.slack.webhook .Values.slack.existingSecret }}
- name: BREVIS_SLACK_WEBHOOK
  valueFrom:
    secretKeyRef:
      name: {{ .Values.slack.existingSecret | default (printf "%s-slack" (include "brevis.fullname" .)) }}
      key: webhook
{{- end }}
{{- if .Values.uiURL }}
- name: BREVIS_UI_URL
  value: {{ .Values.uiURL | quote }}
{{- end }}
{{- end -}}

{{/*
The scrape annotations.

They go on the POD and not on a Service on purpose: the http port is behind the
login, and a scrape endpoint there would either need a session -- which no
scraper has -- or publish every workflow and step name to whoever finds the
path. `metrics` is a separate port for that reason.
*/}}
{{- define "brevis.scrapeAnnotations" -}}
prometheus.io/scrape: "true"
prometheus.io/port: "9090"
prometheus.io/path: "/metrics"
{{- end -}}

{{- define "brevis.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
