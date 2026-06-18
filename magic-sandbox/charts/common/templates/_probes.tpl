{{/*
Copyright Broadcom, Inc. All Rights Reserved.
SPDX-License-Identifier: APACHE-2.0
*/}}

{{/* vim: set filetype=mustache: */}}

{{/*
生成 readinessProbe 配置
用法:
{{ include "common.probes.readiness" (dict "context" . "port" 9501 "path" "/heartbeat") }}
{{ include "common.probes.readiness" (dict "context" . "port" 9501 "path" "/heartbeat" "scheme" "HTTPS") }}
参数 port 和 path 为必传参数，scheme 可选，默认为 HTTP
*/}}
{{- define "common.probes.readiness" -}}
{{- $config := .context.Values.readinessProbe | default dict -}}
readinessProbe:
  failureThreshold: {{ $config.failureThreshold | default 3 }}
  httpGet:
    path: {{ .path }}
    port: {{ .port }}
    scheme: {{ .scheme | default "HTTP" }}
  initialDelaySeconds: {{ $config.initialDelaySeconds | default 5 }}
  periodSeconds: {{ $config.periodSeconds | default 1 }}
  successThreshold: {{ $config.successThreshold | default 1 }}
  timeoutSeconds: {{ $config.timeoutSeconds | default 1 }}
{{- end -}}

{{/*
生成 livenessProbe 配置
用法:
{{ include "common.probes.liveness" (dict "context" . "port" 9501 "path" "/heartbeat") }}
{{ include "common.probes.liveness" (dict "context" . "port" 9501 "path" "/heartbeat" "scheme" "HTTPS") }}
参数 port 和 path 为必传参数，scheme 可选，默认为 HTTP
*/}}
{{- define "common.probes.liveness" -}}
{{- $config := .context.Values.livenessProbe | default dict -}}
livenessProbe:
  failureThreshold: {{ $config.failureThreshold | default 3 }}
  httpGet:
    path: {{ .path }}
    port: {{ .port }}
    scheme: {{ .scheme | default "HTTP" }}
  initialDelaySeconds: {{ $config.initialDelaySeconds | default 5 }}
  periodSeconds: {{ $config.periodSeconds | default 1 }}
  successThreshold: {{ $config.successThreshold | default 1 }}
  timeoutSeconds: {{ $config.timeoutSeconds | default 1 }}
{{- end -}}

{{/*
生成 startupProbe 配置
用法:
{{ include "common.probes.startup" (dict "context" . "port" 9501 "path" "/heartbeat") }}
{{ include "common.probes.startup" (dict "context" . "port" 9501 "path" "/heartbeat" "scheme" "HTTPS") }}
参数 port 和 path 为必传参数，scheme 可选，默认为 HTTP
*/}}
{{- define "common.probes.startup" -}}
{{- $config := .context.Values.startupProbe | default dict -}}
startupProbe:
  failureThreshold: {{ $config.failureThreshold | default 30 }}
  httpGet:
    path: {{ .path }}
    port: {{ .port }}
    scheme: {{ .scheme | default "HTTP" }}
  initialDelaySeconds: {{ $config.initialDelaySeconds | default 10 }}
  periodSeconds: {{ $config.periodSeconds | default 10 }}
  successThreshold: {{ $config.successThreshold | default 1 }}
  timeoutSeconds: {{ $config.timeoutSeconds | default 5 }}
{{- end -}}

