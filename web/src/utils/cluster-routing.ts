import type {
  ArtifactType,
  ClusterFormInput,
  ClusterValidationResult,
  FieldViolation,
  RoutePreviewResult,
  RouteRuleInput,
  RoutingEndpoints,
} from '@/types/cluster';

const IMAGE_MODES: Record<string, true> = { direct: true, pull_through_cache: true, replicated: true };
const CHART_MODES: Record<string, true> = { direct: true, replicated: true };
const PREFIX_PATTERN = /^[a-zA-Z0-9.-]+(?::\d+)?(?:\/[a-zA-Z0-9._~!$&'()*+,;=:@%-]+)*\/?$/;

function addViolation(violations: FieldViolation[], field: string, description: string) {
  violations.push({ field, description });
}

export function isValidPrefix(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.length > 0 && PREFIX_PATTERN.test(trimmed) && !trimmed.includes('..');
}

export function validateClusterForm(input: ClusterFormInput): ClusterValidationResult {
  const violations: FieldViolation[] = [];
  const name = input.name.trim();
  if (name.length === 0) {
    addViolation(violations, 'name', 'Cluster name is required');
  } else if (name.length > 253) {
    addViolation(violations, 'name', 'Cluster name must contain at most 253 characters');
  }

  validateRules(input.imageRules, 'image', 'imageRules', violations);
  validateRules(input.chartRules, 'chart', 'chartRules', violations);
  return { valid: violations.length === 0, violations };
}

function validateRules(
  rules: RouteRuleInput[],
  artifactType: ArtifactType,
  fieldPrefix: 'imageRules' | 'chartRules',
  violations: FieldViolation[],
) {
  const sourceIndexes = new Map<string, number>();
  rules.forEach((rule, index) => {
    const path = `${fieldPrefix}[${index}]`;
    const allowedModes = artifactType === 'image' ? IMAGE_MODES : CHART_MODES;
    if (!allowedModes[rule.mode]) {
      addViolation(
        violations,
        `${path}.mode`,
        artifactType === 'chart'
          ? 'Chart Pull-Through Cache has not passed capability testing'
          : 'Unsupported image routing mode',
      );
    }
    if (!isValidPrefix(rule.sourcePrefix)) {
      addViolation(violations, `${path}.sourcePrefix`, 'Invalid source prefix');
    }
    if (!isValidPrefix(rule.targetPrefix)) {
      addViolation(violations, `${path}.targetPrefix`, 'Invalid target prefix');
    }

    const normalizedSource = rule.sourcePrefix.trim();
    const previousIndex = sourceIndexes.get(normalizedSource);
    if (normalizedSource && previousIndex !== undefined) {
      addViolation(violations, `${path}.sourcePrefix`, 'Route source prefix conflicts with another rule');
      addViolation(
        violations,
        `${fieldPrefix}[${previousIndex}].sourcePrefix`,
        'Route source prefix conflicts with another rule',
      );
    } else if (normalizedSource) {
      sourceIndexes.set(normalizedSource, index);
    }
  });
}

function joinURI(endpoint: string, suffix: string): string {
  return `${endpoint.replace(/\/+$/, '')}/${suffix.replace(/^\/+/, '')}`;
}

export function previewRoute(
  rule: Pick<RouteRuleInput, 'mode' | 'sourcePrefix' | 'targetPrefix'>,
  endpoints: RoutingEndpoints,
): RoutePreviewResult {
  const source = rule.sourcePrefix.trim();
  const target = rule.targetPrefix.trim() || source;
  switch (rule.mode) {
    case 'direct':
      return { centralURI: source, targetURI: target };
    case 'pull_through_cache':
      return {
        centralURI: joinURI(endpoints.cacheEndpoint, source),
        targetURI: target,
      };
    case 'replicated':
      return {
        centralURI: joinURI(endpoints.registryEndpoint, source),
        targetURI: target,
      };
  }
}

export function ruleKey(rule: RouteRuleInput): string {
  return rule.id ?? rule.clientKey;
}
