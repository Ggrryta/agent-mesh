{{/*
Common labels & helpers for agent-mesh chart (4-service architecture).
*/}}

{{- define "agent-mesh.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "agent-mesh.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "agent-mesh.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "agent-mesh.labels" -}}
app.kubernetes.io/name: {{ include "agent-mesh.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "agent-mesh.selectorLabels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
