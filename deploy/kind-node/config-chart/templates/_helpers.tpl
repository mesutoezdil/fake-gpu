{{/* Whether each host port is mapped: the dashboard, and Grafana when the
bundled monitoring stack runs it. */}}
{{- define "kind.dashboard" -}}
{{- if .Values.dashboard.enabled }}true{{ end -}}
{{- end }}

{{- define "kind.grafana" -}}
{{- if and .Values.monitoring.enabled .Values.monitoring.grafana.enabled }}true{{ end -}}
{{- end }}

{{/* A host port mapping for kind's extraPortMappings. */}}
{{- define "kind.port" -}}
- containerPort: {{ .node }}
  hostPort: {{ .host }}
  listenAddress: "127.0.0.1"
  protocol: TCP
{{- end }}
