{{/*
Copyright Broadcom, Inc. All Rights Reserved.
SPDX-License-Identifier: APACHE-2.0
*/}}

{{/* vim: set filetype=mustache: */}}

{{/*
Generate backend entry that is compatible with all Kubernetes API versions.

Usage:
{{ include "common.ingress.backend" (dict "serviceName" "backendName" "servicePort" "backendPort" "context" $) }}

Params:
  - serviceName - String. Name of an existing service backend
  - servicePort - String/Int. Port name (or number) of the service. It will be translated to different yaml depending if it is a string or an integer.
  - context - Dict - Required. The context for the template evaluation.
*/}}
{{- define "common.ingress.backend" -}}
service:
  name: {{ .serviceName }}
  port:
    {{- if typeIs "string" .servicePort }}
    name: {{ .servicePort }}
    {{- else if or (typeIs "int" .servicePort) (typeIs "float64" .servicePort) }}
    number: {{ .servicePort | int }}
    {{- end }}
{{- end -}}

{{/*
Return true if cert-manager required annotations for TLS signed
certificates are set in the Ingress annotations
Ref: https://cert-manager.io/docs/usage/ingress/#supported-annotations
Usage:
{{ include "common.ingress.certManagerRequest" ( dict "annotations" .Values.path.to.the.ingress.annotations ) }}
*/}}
{{- define "common.ingress.certManagerRequest" -}}
{{ if or (hasKey .annotations "cert-manager.io/cluster-issuer") (hasKey .annotations "cert-manager.io/issuer") (hasKey .annotations "kubernetes.io/tls-acme") }}
    {{- true -}}
{{- end -}}
{{- end -}}

{{/*
Render merged ingress annotations from service-level and global-level configurations.
Service-level annotations take precedence over global-level annotations.

Usage:
{{ with include "common.ingress.annotations" . }}
annotations:
  {{- . | nindent 2 }}
{{- end }}

Expects:
  - .Values.ingress.annotations - Optional. Service-level ingress annotations
  - .Values.global.ingress.annotations - Optional. Global-level ingress annotations
*/}}
{{- define "common.ingress.annotations" -}}
{{- $serviceAnnotations := ((.Values).ingress).annotations }}
{{- $globalAnnotations := (((.Values.global).ingress).annotations) }}
{{- if or $serviceAnnotations $globalAnnotations }}
{{- $annotations := include "common.tplvalues.merge" (dict "values" (list $serviceAnnotations $globalAnnotations) "context" .) }}
{{- include "common.tplvalues.render" (dict "value" $annotations "context" .) }}
{{- end }}
{{- end -}}

{{/*
Generate TLS section for an Ingress object.
When TLS is disabled (globally and locally), returns empty string so the caller's {{- with }} block skips it.
Local ingress.tls.enabled takes precise precedence over global (supports explicit false override).

Usage:
{{- with include "common.ingress.tlsSection" (dict "service" "my-svc" "default" "my-svc" "localTls" .Values.ingress.tls "context" .) }}
{{- . | nindent 2 }}
{{- end }}

Params:
  - service:    string  - key in global.services registry
  - default:    string  - fallback subdomain when service not in registry
  - localTls:   dict    - optional, .Values.ingress.tls from the calling chart
  - context:    .       - required
*/}}
{{- define "common.ingress.tlsSection" -}}
{{- $globalTls := (((.context.Values.global).ingress).tls) | default dict -}}
{{- $localTls  := .localTls | default dict -}}
{{- $enabled   := false -}}
{{- if hasKey $localTls "enabled" -}}
  {{- $enabled = index $localTls "enabled" -}}
{{- else -}}
  {{- $enabled = $globalTls.enabled | default false -}}
{{- end -}}
{{- if $enabled -}}
{{- $host       := include "common.utils.serviceHost" (dict "service" .service "default" .default "context" .context) -}}
{{- $secretName := $localTls.secretName | default $globalTls.secretName | default (printf "%s-tls" .service) -}}
tls:
- hosts:
  - {{ $host }}
  secretName: {{ $secretName }}
{{- end -}}
{{- end -}}
