export type ArtifactType = 'image' | 'chart';
export type ImageMode = 'direct' | 'pull_through_cache' | 'replicated';
export type ChartMode = 'direct' | 'replicated';
export type RouteMode = ImageMode | ChartMode;

export interface RouteRuleInput {
  id?: string;
  clientKey: string;
  artifactType: ArtifactType;
  mode: RouteMode;
  sourcePrefix: string;
  targetPrefix: string;
  provider?: string;
}

export interface ClusterFormInput {
  name: string;
  enabled: boolean;
  version: number;
  imageRules: RouteRuleInput[];
  chartRules: RouteRuleInput[];
}

export interface ClusterSummary {
  id: string;
  name: string;
  customerId: string;
  enabled: boolean;
  version: number;
  routeCount: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface Cluster extends ClusterSummary {
  imageRules: RouteRuleInput[];
  chartRules: RouteRuleInput[];
}

export interface FieldViolation {
  field: string;
  description: string;
}

export interface SaveError {
  code: string;
  message: string;
  fieldViolations?: FieldViolation[];
  conflictingRuleId?: string;
}

export interface RoutingEndpoints {
  cacheEndpoint: string;
  registryEndpoint: string;
}

export interface RoutePreviewResult {
  centralURI: string;
  targetURI: string;
}

export interface ClusterValidationResult {
  valid: boolean;
  violations: FieldViolation[];
}
