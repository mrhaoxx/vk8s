{{/*
通用模板片段
*/}}

{{- define "vk8s.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "vk8s.labels" -}}
app.kubernetes.io/name: vk8s
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* 用法: include "vk8s.selectorLabels" (dict "component" "manager" "root" .) */}}
{{- define "vk8s.selectorLabels" -}}
app.kubernetes.io/name: vk8s
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "vk8s.annotations" -}}
{{- with .Values.commonAnnotations }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "vk8s.publicURL" -}}
{{- $scheme := ternary "https" "http" .Values.manager.ingress.tls.enabled -}}
{{ .Values.publicURL | default (printf "%s://console.%s" $scheme .Values.apiDomain) }}
{{- end -}}

{{- define "vk8s.consoleHost" -}}
{{ .Values.manager.ingress.host | default (printf "console.%s" .Values.apiDomain) }}
{{- end -}}

{{- define "vk8s.bootstrapAdminSecretName" -}}
{{ .Values.auth.bootstrapAdmin.existingSecret | default "bootstrap-admin" }}
{{- end -}}

{{- define "vk8s.oidcSecretName" -}}
{{ .Values.auth.oidc.existingSecret | default "oidc-client" }}
{{- end -}}

{{- define "vk8s.clickhouseSecretName" -}}
{{- default "vk8s-clickhouse" .Values.audit.clickhouse.existingSecret -}}
{{- end -}}

{{- define "vk8s.clickhouseURL" -}}
{{- if .Values.audit.clickhouse.url -}}
{{- .Values.audit.clickhouse.url -}}
{{- else -}}
http://vk8s-clickhouse.{{ .Release.Namespace }}.svc:8123
{{- end -}}
{{- end -}}

{{- define "vk8s.managerServiceAccount" -}}
{{ .Values.serviceAccount.manager.name | default "vk8s-manager" }}
{{- end -}}

{{- define "vk8s.gatewayServiceAccount" -}}
{{ .Values.serviceAccount.gateway.name | default "vk8s-gateway" }}
{{- end -}}
