{{/*
License volume mount configuration
Usage:
{{ include "common.license.volumeMount" . | nindent 8 }}
*/}}
{{- define "common.license.volumeMount" -}}
{{- $global := .Values.global | default dict -}}
{{- $license := $global.license | default dict -}}
{{- if $license.enabled -}}
{{- $fileName := $license.fileName | default "tankee_license" -}}
{{- $mountPath := $license.mountPath | default "/etc" -}}
- name: license
  mountPath: {{ printf "%s/%s" $mountPath $fileName | quote }}
  subPath: {{ $fileName | quote }}
  readOnly: true
{{- end -}}
{{- end -}}

{{/*
License volume configuration
Usage:
{{ include "common.license.volume" . | nindent 6 }}
*/}}
{{- define "common.license.volume" -}}
{{- $global := .Values.global | default dict -}}
{{- $license := $global.license | default dict -}}
{{- if $license.enabled -}}
- name: license
  secret:
    secretName: {{ $license.secretName | default "tankee-license" | quote }}
    items:
    - key: {{ $license.fileName | default "tankee_license" | quote }}
      path: {{ $license.fileName | default "tankee_license" | quote }}
{{- end -}}
{{- end -}}

