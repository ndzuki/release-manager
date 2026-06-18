{{/*
Expand the name of the chart.
*/}}
{{- define "sandbox-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "sandbox-gateway.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "sandbox-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "sandbox-gateway.labels" -}}
helm.sh/chart: {{ include "sandbox-gateway.chart" . }}
{{ include "sandbox-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sandbox-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sandbox-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "sandbox-gateway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sandbox-gateway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
将 Kubernetes 风格的 resources（requests/limits 的 cpu、memory）渲染为 ConfigMap data 中的环境变量行。
仅在配置了 resources 时输出；每行两空格缩进，便于挂在 `data:` 下。

调用示例：
  {{- include "sandbox-gateway.configmapWorkloadResourceEnv" (dict "resources" $agent.resources "prefix" "AGENT") }}

参数（dict）：
  resources — 与 Pod resources 相同结构的对象，可为 nil
  prefix    — 环境变量前缀：AGENT、FUSE 等
*/}}
{{- define "sandbox-gateway.configmapWorkloadResourceEnv" -}}
{{- $p := .prefix }}
{{- with .resources }}
{{- with .requests }}
{{- if .cpu }}
  {{ $p }}_CPU_REQUEST: {{ .cpu | quote }}
{{- end }}
{{- if .memory }}
  {{ $p }}_MEMORY_REQUEST: {{ .memory | quote }}
{{- end }}
{{- end }}
{{- with .limits }}
{{- if .cpu }}
  {{ $p }}_CPU_LIMIT: {{ .cpu | quote }}
{{- end }}
{{- if .memory }}
  {{ $p }}_MEMORY_LIMIT: {{ .memory | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

