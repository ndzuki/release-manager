{{/*
Copyright Broadcom, Inc. All Rights Reserved.
SPDX-License-Identifier: APACHE-2.0
*/}}

{{/* vim: set filetype=mustache: */}}

{{/*
Return a resource request/limit object based on a given preset.
These presets are for basic testing and not meant to be used in production
{{ include "common.resources.preset" (dict "type" "nano") -}}
*/}}
{{- define "common.resources.preset" -}}
{{/* The limits are the requests increased by 50% (except ephemeral-storage and xlarge/2xlarge sizes)*/}}
{{- $presets := dict 
  "nano" (dict 
      "requests" (dict "cpu" "100m" "memory" "128Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "150m" "memory" "192Mi" "ephemeral-storage" "2Gi")
   )
  "micro" (dict 
      "requests" (dict "cpu" "250m" "memory" "256Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "375m" "memory" "384Mi" "ephemeral-storage" "2Gi")
   )
  "small" (dict 
      "requests" (dict "cpu" "500m" "memory" "512Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "750m" "memory" "768Mi" "ephemeral-storage" "2Gi")
   )
  "medium" (dict 
      "requests" (dict "cpu" "500m" "memory" "1024Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "750m" "memory" "1536Mi" "ephemeral-storage" "2Gi")
   )
  "large" (dict 
      "requests" (dict "cpu" "1.0" "memory" "2048Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "1.5" "memory" "3072Mi" "ephemeral-storage" "2Gi")
   )
  "xlarge" (dict 
      "requests" (dict "cpu" "1.0" "memory" "3072Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "3.0" "memory" "6144Mi" "ephemeral-storage" "2Gi")
   )
  "2xlarge" (dict 
      "requests" (dict "cpu" "1.0" "memory" "3072Mi" "ephemeral-storage" "50Mi")
      "limits" (dict "cpu" "6.0" "memory" "12288Mi" "ephemeral-storage" "2Gi")
   )
 }}
{{- if hasKey $presets .type -}}
{{- index $presets .type | toYaml -}}
{{- else -}}
{{- printf "ERROR: Preset key '%s' invalid. Allowed values are %s" .type (join "," (keys $presets)) | fail -}}
{{- end -}}
{{- end -}}

{{/*
Calculate resources based on preset, profile, and overrides
Usage:
  {{- include "common.resources.calculate" (dict 
      "context" .
      "preset" (.Values.resources.preset | default "")
      "resources" .Values.resources
      "overrides" (.Values.resources.overrides | default dict)
  ) | nindent 10 }}
*/}}
{{- define "common.resources.calculate" -}}
{{- $context := .context -}}
{{- $preset := .preset -}}
{{- $resources := .resources -}}
{{- $overrides := .overrides -}}
{{- $global := $context.Values.global | default dict -}}

{{- /* If resources contains requests/limits directly, use them (backward compatibility) */ -}}
{{- if and $resources (or (hasKey $resources "requests") (hasKey $resources "limits")) -}}
{{- $resources | toYaml -}}
{{- else -}}
  {{- /* Get active resource profile */ -}}
  {{- $activeProfile := $global.activeResourceProfile | default "test" -}}
  {{- $profiles := $global.resourceProfiles | default dict -}}
  {{- $profile := index $profiles $activeProfile | default dict -}}
  {{- /* Use 1.5 if overcommitRatio is missing or 0 */ -}}
  {{- $overcommitRatio := 1.5 -}}
  {{- if $profile.overcommitRatio -}}
    {{- if gt ($profile.overcommitRatio | float64) 0.0 -}}
      {{- $overcommitRatio = $profile.overcommitRatio | float64 -}}
    {{- end -}}
  {{- end -}}
  {{- $defaultPreset := $profile.defaultPreset | default "nano" -}}
  
  {{- /* Determine which preset to use */ -}}
  {{- $usePreset := $preset | default $defaultPreset -}}
  
  {{- /* Define built-in default presets (fallback) */ -}}
  {{- $builtinPresets := dict 
    "nano" (dict 
        "requests" (dict "cpu" "50m" "memory" "128Mi" "ephemeral-storage" "50Mi")
        "ephemeralLimit" "1Gi"
     )
    "micro" (dict 
        "requests" (dict "cpu" "100m" "memory" "256Mi" "ephemeral-storage" "50Mi")
        "ephemeralLimit" "1Gi"
     )
    "small" (dict 
        "requests" (dict "cpu" "250m" "memory" "512Mi" "ephemeral-storage" "100Mi")
        "ephemeralLimit" "1Gi"
     )
    "medium" (dict 
        "requests" (dict "cpu" "500m" "memory" "1Gi" "ephemeral-storage" "100Mi")
        "ephemeralLimit" "2Gi"
     )
    "large" (dict 
        "requests" (dict "cpu" "1" "memory" "2Gi" "ephemeral-storage" "200Mi")
        "ephemeralLimit" "2Gi"
     )
    "xlarge" (dict 
        "requests" (dict "cpu" "2" "memory" "4Gi" "ephemeral-storage" "200Mi")
        "ephemeralLimit" "3Gi"
     )
    "2xlarge" (dict 
        "requests" (dict "cpu" "4" "memory" "8Gi" "ephemeral-storage" "500Mi")
        "ephemeralLimit" "5Gi"
     )
    "unlimit" (dict 
        "requests" (dict "cpu" "50m" "memory" "128Mi" "ephemeral-storage" "50Mi")
        "ephemeralLimit" "1Gi"
        "noLimits" true
     )
   }}
  
  {{- /* Try to get custom presets from global.resourcePresets */ -}}
  {{- $customPresets := $global.resourcePresets | default dict -}}
  
  {{- /* Merge mode: Use custom preset if exists, otherwise use built-in default */ -}}
  {{- $baseResources := dict -}}
  {{- if and $customPresets (hasKey $customPresets $usePreset) -}}
    {{- /* Use custom preset from values.yaml */ -}}
    {{- $baseResources = index $customPresets $usePreset -}}
  {{- else if hasKey $builtinPresets $usePreset -}}
    {{- /* Use built-in default preset */ -}}
    {{- $baseResources = index $builtinPresets $usePreset -}}
  {{- else -}}
    {{- fail (printf "Invalid preset '%s'. Allowed values: nano, micro, small, medium, large, xlarge, 2xlarge" $usePreset) -}}
  {{- end -}}
  
  {{- $baseRequests := $baseResources.requests -}}
  {{- $ephemeralLimit := $baseResources.ephemeralLimit | default "2Gi" -}}
  
  {{- /* Apply overrides to requests */ -}}
  {{- $finalRequests := $baseRequests -}}
  {{- if $overrides -}}
    {{- if hasKey $overrides "requests" -}}
      {{- $finalRequests = merge (deepCopy ($overrides.requests | default dict)) $baseRequests -}}
    {{- end -}}
  {{- end -}}
  
  {{- /* Calculate limits based on overcommit ratio */ -}}
  {{- $cpuRequest := index $finalRequests "cpu" | default "100m" -}}
  {{- $memRequest := index $finalRequests "memory" | default "128Mi" -}}
  {{- $ephemeralRequest := index $finalRequests "ephemeral-storage" | default "50Mi" -}}
  
  {{- /* Parse CPU (handle 'm' suffix and numeric values) */ -}}
  {{- $cpuValue := 0.0 -}}
  {{- if hasSuffix "m" $cpuRequest -}}
    {{- $cpuValue = divf (trimSuffix "m" $cpuRequest | float64) 1000.0 -}}
  {{- else -}}
    {{- $cpuValue = $cpuRequest | float64 -}}
  {{- end -}}
  {{- $cpuLimit := mulf $cpuValue $overcommitRatio -}}
  
  {{- /* Parse Memory (handle Mi/Gi suffix) */ -}}
  {{- $memValue := 0.0 -}}
  {{- $memUnit := "Mi" -}}
  {{- if hasSuffix "Gi" $memRequest -}}
    {{- $memValue = trimSuffix "Gi" $memRequest | float64 -}}
    {{- $memUnit = "Gi" -}}
  {{- else if hasSuffix "Mi" $memRequest -}}
    {{- $memValue = trimSuffix "Mi" $memRequest | float64 -}}
    {{- $memUnit = "Mi" -}}
  {{- end -}}
  {{- $memLimit := mulf $memValue $overcommitRatio -}}
  
  {{- /* Format limits */ -}}
  {{- $cpuLimitStr := "" -}}
  {{- if ge $cpuLimit 1.0 -}}
    {{- $cpuLimitStr = printf "%.1f" $cpuLimit -}}
  {{- else -}}
    {{- $cpuLimitStr = printf "%.0fm" (mulf $cpuLimit 1000.0) -}}
  {{- end -}}
  
  {{- $memLimitStr := printf "%.0f%s" $memLimit $memUnit -}}
  
  {{- $finalLimits := dict 
      "cpu" $cpuLimitStr
      "memory" $memLimitStr
      "ephemeral-storage" $ephemeralLimit
  -}}
  
  {{- /* Apply overrides to limits */ -}}
  {{- if $overrides -}}
    {{- if hasKey $overrides "limits" -}}
      {{- $finalLimits = merge (deepCopy ($overrides.limits | default dict)) $finalLimits -}}
    {{- end -}}
  {{- end -}}
  
  {{- /* Check if this preset should skip limits */ -}}
  {{- $noLimits := $baseResources.noLimits | default false -}}
  
  {{- /* Output final resources */ -}}
requests:
  cpu: {{ $finalRequests.cpu | quote }}
  memory: {{ $finalRequests.memory | quote }}
  ephemeral-storage: {{ index $finalRequests "ephemeral-storage" | quote }}
{{- if not $noLimits }}
limits:
  cpu: {{ $finalLimits.cpu | quote }}
  memory: {{ $finalLimits.memory | quote }}
  ephemeral-storage: {{ index $finalLimits "ephemeral-storage" | quote }}
{{- end -}}
{{- end -}}
{{- end -}}
