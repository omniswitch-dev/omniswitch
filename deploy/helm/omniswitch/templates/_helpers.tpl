{{/*
Expand the name of the chart.
*/}}
{{- define "omniswitch.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "omniswitch.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "omniswitch.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "omniswitch.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Name of the Secret holding provider credentials.
*/}}
{{- define "omniswitch.secretName" -}}
{{- if .Values.existingSecret }}
{{- .Values.existingSecret }}
{{- else }}
{{- include "omniswitch.fullname" . }}-secrets
{{- end }}
{{- end }}

{{/* Name of the separate Secret holding the database URL. */}}
{{- define "omniswitch.databaseSecretName" -}}
{{- default (printf "%s-database" (include "omniswitch.fullname" .)) .Values.database.existingSecret }}
{{- end }}

{{/* External Redis URL or the release-specific bundled Redis service. */}}
{{- define "omniswitch.redisURL" -}}
{{- default (printf "redis://%s-redis:6379/0" (include "omniswitch.fullname" .)) .Values.redis.url }}
{{- end }}

{{/* Use the namespace default ServiceAccount when creation is disabled. */}}
{{- define "omniswitch.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "omniswitch.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
