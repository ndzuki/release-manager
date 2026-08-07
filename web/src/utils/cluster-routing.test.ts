import { describe, expect, it } from 'vitest';
import type { ClusterFormInput, RouteRuleInput, RoutingEndpoints } from '@/types/cluster';
import { previewRoute, validateClusterForm } from './cluster-routing';

const endpoints: RoutingEndpoints = {
  cacheEndpoint: 'cache.example.com',
  registryEndpoint: 'registry.example.com',
};

function route(overrides: Partial<RouteRuleInput> = {}): RouteRuleInput {
  return {
    clientKey: crypto.randomUUID(),
    artifactType: 'image',
    mode: 'direct',
    sourcePrefix: 'docker.io/library/',
    targetPrefix: 'harbor.example.com/proxy/',
    ...overrides,
  };
}

function form(overrides: Partial<ClusterFormInput> = {}): ClusterFormInput {
  return {
    name: 'staging',
    enabled: true,
    version: 1,
    imageRules: [],
    chartRules: [],
    ...overrides,
  };
}

describe('validateClusterForm', () => {
  it('locates both rules with the same source prefix', () => {
    const result = validateClusterForm(form({
      imageRules: [
        route({ clientKey: 'first' }),
        route({ clientKey: 'second', targetPrefix: 'harbor.example.com/other/' }),
      ],
    }));

    expect(result.valid).toBe(false);
    expect(result.violations).toEqual(expect.arrayContaining([
      { field: 'imageRules[0].sourcePrefix', description: 'Route source prefix conflicts with another rule' },
      { field: 'imageRules[1].sourcePrefix', description: 'Route source prefix conflicts with another rule' },
    ]));
  });

  it('rejects unsupported Chart pull-through cache mode', () => {
    const result = validateClusterForm(form({
      chartRules: [route({ artifactType: 'chart', mode: 'pull_through_cache' })],
    }));

    expect(result.violations).toContainEqual({
      field: 'chartRules[0].mode',
      description: 'Chart Pull-Through Cache has not passed capability testing',
    });
  });

  it.each([
    ['', 'Cluster name is required'],
    ['x'.repeat(254), 'Cluster name must contain at most 253 characters'],
  ])('rejects invalid cluster name boundary %#', (name, description) => {
    const result = validateClusterForm(form({ name }));

    expect(result.violations).toContainEqual({ field: 'name', description });
  });
});

describe('previewRoute', () => {
  it.each([
    ['direct', 'docker.io/library/', 'harbor.example.com/proxy/'],
    ['pull_through_cache', 'cache.example.com/docker.io/library/', 'harbor.example.com/proxy/'],
    ['replicated', 'registry.example.com/docker.io/library/', 'harbor.example.com/proxy/'],
  ] as const)('maps %s deterministically', (mode, centralURI, targetURI) => {
    expect(previewRoute(route({ mode }), endpoints)).toEqual({ centralURI, targetURI });
  });
});
