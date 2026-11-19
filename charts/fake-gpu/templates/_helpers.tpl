{{/*
Names. Every object is named from fake-gpu.fullname, so a release called
fake-gpu yields fake-gpu, fake-gpu-metrics, fake-gpu-metrics-exporter and so on.
*/}}
{{- define "fake-gpu.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "fake-gpu.fullname" -}}
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

{{- define "fake-gpu.exporterName" -}}
{{- include "fake-gpu.fullname" . }}-metrics-exporter
{{- end }}

{{/* Labels. */}}
{{- define "fake-gpu.labels" -}}
app.kubernetes.io/name: {{ include "fake-gpu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "fake-gpu.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fake-gpu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "fake-gpu.exporterSelectorLabels" -}}
app.kubernetes.io/name: {{ include "fake-gpu.exporterName" . }}
{{- end }}

{{/* The agent image; the NRI plugin ships in the same image. */}}
{{- define "fake-gpu.image" -}}
"{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
{{- end }}

{{/* Pod nodeSelector block, indented for a pod spec; empty when unset. */}}
{{- define "fake-gpu.nodeSelector" -}}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}

{{/* Fixed startup partition mode; older releases may carry an empty value. */}}
{{- define "fake-gpu.partition" -}}
{{- .Values.gpu.partition | default "SPX" }}
{{- end }}
