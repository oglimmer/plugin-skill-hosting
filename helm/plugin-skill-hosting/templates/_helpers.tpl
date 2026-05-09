{{/*
Expand the name of the chart.
*/}}
{{- define "psh.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "psh.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label
*/}}
{{- define "psh.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "psh.labels" -}}
helm.sh/chart: {{ include "psh.chart" . }}
{{ include "psh.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "psh.selectorLabels" -}}
app.kubernetes.io/name: {{ include "psh.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Backend
*/}}
{{- define "psh.backend.fullname" -}}
{{- printf "%s-backend" (include "psh.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "psh.backend.labels" -}}
helm.sh/chart: {{ include "psh.chart" . }}
{{ include "psh.backend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: backend
{{- end }}

{{- define "psh.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "psh.name" . }}-backend
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: backend
{{- end }}

{{/*
Frontend
*/}}
{{- define "psh.frontend.fullname" -}}
{{- printf "%s-frontend" (include "psh.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "psh.frontend.labels" -}}
helm.sh/chart: {{ include "psh.chart" . }}
{{ include "psh.frontend.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: frontend
{{- end }}

{{- define "psh.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "psh.name" . }}-frontend
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: frontend
{{- end }}

{{/*
Postgres
*/}}
{{- define "psh.postgres.fullname" -}}
{{- printf "%s-postgres" (include "psh.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "psh.postgres.labels" -}}
helm.sh/chart: {{ include "psh.chart" . }}
{{ include "psh.postgres.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: postgres
{{- end }}

{{- define "psh.postgres.selectorLabels" -}}
app.kubernetes.io/name: {{ include "psh.name" . }}-postgres
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: postgres
{{- end }}

{{/*
Service account name
*/}}
{{- define "psh.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "psh.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name (sealed-secret holds JWT_SECRET, POSTGRES_PASSWORD, optional DATABASE_URL)
*/}}
{{- define "psh.secretName" -}}
{{- printf "%s-secret" (include "psh.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
