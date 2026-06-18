{{/*
Copyright Broadcom, Inc. All Rights Reserved.
SPDX-License-Identifier: APACHE-2.0
*/}}

{{/* vim: set filetype=mustache: */}}
{{/*
Print instructions to get a secret value.
Usage:
{{ include "common.utils.secret.getvalue" (dict "secret" "secret-name" "field" "secret-value-field" "context" $) }}
*/}}
{{- define "common.utils.secret.getvalue" -}}
{{- $varname := include "common.utils.fieldToEnvVar" . -}}
export {{ $varname }}=$(kubectl get secret --namespace {{ include "common.names.namespace" .context | quote }} {{ .secret }} -o jsonpath="{.data.{{ .field }}}" | base64 -d)
{{- end -}}

{{/*
Build env var name given a field
Usage:
{{ include "common.utils.fieldToEnvVar" dict "field" "my-password" }}
*/}}
{{- define "common.utils.fieldToEnvVar" -}}
  {{- $fieldNameSplit := splitList "-" .field -}}
  {{- $upperCaseFieldNameSplit := list -}}

  {{- range $fieldNameSplit -}}
    {{- $upperCaseFieldNameSplit = append $upperCaseFieldNameSplit ( upper . ) -}}
  {{- end -}}

  {{ join "_" $upperCaseFieldNameSplit }}
{{- end -}}

{{/*
Gets a value from .Values given
Usage:
{{ include "common.utils.getValueFromKey" (dict "key" "path.to.key" "context" $) }}
*/}}
{{- define "common.utils.getValueFromKey" -}}
{{- $splitKey := splitList "." .key -}}
{{- $value := "" -}}
{{- $latestObj := $.context.Values -}}
{{- range $splitKey -}}
  {{- if not $latestObj -}}
    {{- printf "please review the entire path of '%s' exists in values" $.key | fail -}}
  {{- end -}}
  {{- $value = ( index $latestObj . ) -}}
  {{- $latestObj = $value -}}
{{- end -}}
{{- printf "%v" (default "" $value) -}} 
{{- end -}}

{{/*
Returns first .Values key with a defined value or first of the list if all non-defined
Usage:
{{ include "common.utils.getKeyFromList" (dict "keys" (list "path.to.key1" "path.to.key2") "context" $) }}
*/}}
{{- define "common.utils.getKeyFromList" -}}
{{- $key := first .keys -}}
{{- $reverseKeys := reverse .keys }}
{{- range $reverseKeys }}
  {{- $value := include "common.utils.getValueFromKey" (dict "key" . "context" $.context ) }}
  {{- if $value -}}
    {{- $key = . }}
  {{- end -}}
{{- end -}}
{{- printf "%s" $key -}} 
{{- end -}}

{{/*
Checksum a template at "path" containing a *single* resource (ConfigMap,Secret) for use in pod annotations, excluding the metadata (see #18376).
Usage:
{{ include "common.utils.checksumTemplate" (dict "path" "/configmap.yaml" "context" $) }}
*/}}
{{- define "common.utils.checksumTemplate" -}}
{{- $obj := include (print .context.Template.BasePath .path) .context | fromYaml -}}
{{ omit $obj "apiVersion" "kind" "metadata" | toYaml | sha256sum }}
{{- end -}}

{{/*
Generate HTTP or HTTPS protocol based on TLS configuration
Usage:
{{ include "common.utils.httpProtocol" . }}
*/}}
{{- define "common.utils.httpProtocol" -}}
{{- if and .Values.global .Values.global.ingress .Values.global.ingress.tls .Values.global.ingress.tls.enabled -}}
https
{{- else -}}
http
{{- end -}}
{{- end -}}

{{/*
Generate WS or WSS protocol based on TLS configuration
Usage:
{{ include "common.utils.wsProtocol" . }}
*/}}
{{- define "common.utils.wsProtocol" -}}
{{- if and .Values.global .Values.global.ingress .Values.global.ingress.tls .Values.global.ingress.tls.enabled -}}
wss
{{- else -}}
ws
{{- end -}}
{{- end -}}

{{/*
Generate full URL with dynamic protocol based on TLS configuration
Usage:
{{ include "common.utils.httpUrl" (dict "subdomain" "api" "context" $) }}
{{ include "common.utils.httpUrl" (dict "subdomain" "teamshare" "context" $) }}
*/}}
{{- define "common.utils.httpUrl" -}}
{{- $protocol := include "common.utils.httpProtocol" .context -}}
{{- $global := .context.Values.global | default dict -}}
{{- $domainSuffix := $global.domainSuffix | default "example.local" -}}
{{- printf "%s://%s.%s" $protocol .subdomain $domainSuffix -}}
{{- end -}}

{{/*
Generate full WebSocket URL with dynamic protocol based on TLS configuration
Usage:
{{ include "common.utils.wsUrl" (dict "subdomain" "teamshare-service" "context" $) }}
*/}}
{{- define "common.utils.wsUrl" -}}
{{- $protocol := include "common.utils.wsProtocol" .context -}}
{{- $global := .context.Values.global | default dict -}}
{{- $domainSuffix := $global.domainSuffix | default "example.local" -}}
{{- printf "%s://%s.%s" $protocol .subdomain $domainSuffix -}}
{{- end -}}

{{/*
返回纯 host 字符串（不含协议），供 ingress rules.host 字段使用。
sharedDomain 模式下，有 path 字段的服务返回共享聚合域名；否则沿用原有逻辑。
优先级（非 sharedDomain）：global.services.<service>.domain > global.services.<service>.subdomain+domainSuffix > default+domainSuffix
Usage:
{{ include "common.utils.serviceHost" (dict "service" "vcm-api" "default" "vcm-api" "context" $) }}
*/}}
{{- define "common.utils.serviceHost" -}}
{{- $global := .context.Values.global | default dict -}}
{{- $sharedDomain := (($global.ingress).sharedDomain) | default dict -}}
{{- $services := $global.services | default dict -}}
{{- $cfg := index $services .service | default dict -}}
{{- if and $sharedDomain.enabled $cfg.path -}}
{{- $sharedSubdomain := $sharedDomain.domain | default "api" -}}
{{- $domainSuffix := $global.domainSuffix | default "example.local" -}}
{{- printf "%s.%s" $sharedSubdomain $domainSuffix -}}
{{- else if $cfg.domain -}}
{{- $cfg.domain -}}
{{- else -}}
{{- $subdomain := $cfg.subdomain | default (.default | default .service) -}}
{{- $domainSuffix := $global.domainSuffix | default "example.local" -}}
{{- printf "%s.%s" $subdomain $domainSuffix -}}
{{- end -}}
{{- end -}}

{{/*
返回服务在 sharedDomain 模式下的完整路径前缀（含 global.ingress.sharedDomain.prefix）。
非 sharedDomain 模式或服务未配置 path 时返回空字符串。
供 serviceHttpUrl / serviceWsUrl 拼接 URL，以及 ingress 模板构造路径使用。
Usage:
{{ include "common.utils.serviceBasePath" (dict "service" "vcm-api" "context" $) }}
{{- 返回示例："/_/vcm-api" 或 "" -}}
*/}}
{{- define "common.utils.serviceBasePath" -}}
{{- $global := .context.Values.global | default dict -}}
{{- $sharedDomain := (($global.ingress).sharedDomain) | default dict -}}
{{- $services := $global.services | default dict -}}
{{- $cfg := index $services .service | default dict -}}
{{- if and $sharedDomain.enabled $cfg.path -}}
{{- $prefix := $sharedDomain.prefix | default "/_" -}}
{{- printf "%s%s" $prefix $cfg.path -}}
{{- end -}}
{{- end -}}

{{/*
返回 public URL 需要追加的端口后缀（如 ":30080"）。
仅在 sharedDomain + publicPorts 同时启用时生效；标准端口、无效端口和非数字值不拼接。
Usage:
{{ include "common.utils.servicePortSuffix" (dict "scheme" "http" "context" $) }}
*/}}
{{- define "common.utils.servicePortSuffix" -}}
{{- $global := .context.Values.global | default dict -}}
{{- $sharedDomain := (($global.ingress).sharedDomain) | default dict -}}
{{- $publicPorts := $sharedDomain.publicPorts | default dict -}}
{{- if and $sharedDomain.enabled $publicPorts.enabled -}}
{{- $scheme := .scheme | default "" -}}
{{- $portKey := "" -}}
{{- $standardPort := 0 -}}
{{- if or (eq $scheme "http") (eq $scheme "ws") -}}
  {{- $portKey = "http" -}}
  {{- $standardPort = 80 -}}
{{- else if or (eq $scheme "https") (eq $scheme "wss") -}}
  {{- $portKey = "https" -}}
  {{- $standardPort = 443 -}}
{{- end -}}
{{- if and $portKey (hasKey $publicPorts $portKey) -}}
  {{- $rawPort := get $publicPorts $portKey -}}
  {{- $portText := printf "%v" $rawPort -}}
  {{- if regexMatch "^[0-9]+$" $portText -}}
    {{- $port := int $portText -}}
    {{- if and (gt $port 0) (le $port 65535) (ne $port $standardPort) -}}
    {{- printf ":%d" $port -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
生成 HTTP/HTTPS URL，从 global.services 注册表读取 host。
sharedDomain 模式下 URL 含路径前缀（如 http://api.local/_/vcm-api），否则仅含 host。
Usage:
{{ include "common.utils.serviceHttpUrl" (dict "service" "vcm-api" "default" "vcm-api" "context" $) }}
*/}}
{{- define "common.utils.serviceHttpUrl" -}}
{{- $protocol := include "common.utils.httpProtocol" .context -}}
{{- $host := include "common.utils.serviceHost" . -}}
{{- $portSuffix := include "common.utils.servicePortSuffix" (dict "scheme" $protocol "context" .context) -}}
{{- $basePath := include "common.utils.serviceBasePath" . -}}
{{- printf "%s://%s%s%s" $protocol $host $portSuffix $basePath -}}
{{- end -}}

{{/*
生成 WS/WSS URL，从 global.services 注册表读取 host。
sharedDomain 模式下 URL 含路径前缀（如 ws://api.local/_/vcm-api），否则仅含 host。
Usage:
{{ include "common.utils.serviceWsUrl" (dict "service" "vcm-api" "default" "vcm-api" "context" $) }}
*/}}
{{- define "common.utils.serviceWsUrl" -}}
{{- $protocol := include "common.utils.wsProtocol" .context -}}
{{- $host := include "common.utils.serviceHost" . -}}
{{- $portSuffix := include "common.utils.servicePortSuffix" (dict "scheme" $protocol "context" .context) -}}
{{- $basePath := include "common.utils.serviceBasePath" . -}}
{{- printf "%s://%s%s%s" $protocol $host $portSuffix $basePath -}}
{{- end -}}

{{/*
返回 Ingress rules 中 path 字段的值，内部根据 sharedDomain 模式自动切换格式。
sharedDomain 开：{prefix}{servicePath}{trimSuffix "/" path}(/|$)(.*)
sharedDomain 关：{path}（原样返回）
调用方始终使用 pathType: ImplementationSpecific（sharedDomain 模式下捕获组要求；关闭时与 Prefix 等价）。
Usage:
path: {{ include "common.utils.ingressPath" (dict "service" "vcm-api" "path" "/" "context" $) }}
path: {{ include "common.utils.ingressPath" (dict "service" "keewood-service" "path" "/socket.io" "context" $) }}
*/}}
{{- define "common.utils.ingressPath" -}}
{{- $basePath := include "common.utils.serviceBasePath" (dict "service" .service "context" .context) -}}
{{- if $basePath -}}
{{- $trimmedPath := trimSuffix "/" .path -}}
{{- printf "%s%s(/|$)(.*)" $basePath $trimmedPath -}}
{{- else -}}
{{- .path -}}
{{- end -}}
{{- end -}}

{{/*
返回 nginx rewrite-target 注解整行（或空字符串），供 with 语法按需渲染。
sharedDomain 开：nginx.ingress.kubernetes.io/rewrite-target: {trimSuffix "/" path}/$2
sharedDomain 关：""（with 块自动跳过）
Usage:
{{- with include "common.utils.ingressRewriteAnnotation" (dict "service" "vcm-api" "path" "/" "context" $) }}
{{- . | nindent 4 }}
{{- end }}
*/}}
{{- define "common.utils.ingressRewriteAnnotation" -}}
{{- $basePath := include "common.utils.serviceBasePath" (dict "service" .service "context" .context) -}}
{{- if $basePath -}}
{{- $trimmedPath := trimSuffix "/" .path -}}
{{- printf "nginx.ingress.kubernetes.io/rewrite-target: %s/$2" $trimmedPath -}}
{{- end -}}
{{- end -}}
