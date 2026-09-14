{{- define "geocam-edge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "geocam-edge.fullname" -}}
{{- printf "%s" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "geocam-edge.labels" -}}
app.kubernetes.io/name: {{ include "geocam-edge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "geocam-edge.selectorLabels" -}}
app.kubernetes.io/name: {{ include "geocam-edge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
