<script setup lang="ts">
import { computed } from 'vue';
import type { RouteRuleInput, RoutingEndpoints } from '@/types/cluster';
import { previewRoute } from '@/utils/cluster-routing';

const props = defineProps<{
  rule: RouteRuleInput;
  endpoints: RoutingEndpoints;
}>();

const preview = computed(() => previewRoute(props.rule, props.endpoints));
</script>

<template>
  <dl class="route-preview" aria-label="Route preview">
    <div>
      <dt>Central URI</dt>
      <dd data-testid="central-uri">{{ preview.centralURI || 'Enter a source prefix' }}</dd>
    </div>
    <div>
      <dt>Target URI</dt>
      <dd data-testid="target-uri">{{ preview.targetURI || 'Enter a target prefix' }}</dd>
    </div>
  </dl>
</template>

<style scoped>
.route-preview {
  display: grid;
  gap: 0.5rem;
  margin: 0;
  padding: 0.75rem;
  border-radius: 0.5rem;
  background: #f8fafc;
}
.route-preview div { display: grid; grid-template-columns: 7rem 1fr; gap: 0.75rem; }
.route-preview dt { color: #475569; font-weight: 600; }
.route-preview dd { margin: 0; overflow-wrap: anywhere; font-family: monospace; }
</style>
