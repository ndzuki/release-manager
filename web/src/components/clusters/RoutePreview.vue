<script setup lang="ts">
import { t } from '@/i18n/messages';
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
  <dl class="route-preview" :aria-label="t('route.preview.title')">
    <div>
      <dt>{{ t('route.preview.centralUri') }}</dt>
      <dd data-testid="central-uri">{{ preview.centralURI || t('route.preview.sourcePlaceholder') }}</dd>
    </div>
    <div>
      <dt>{{ t('route.preview.targetUri') }}</dt>
      <dd data-testid="target-uri">{{ preview.targetURI || t('route.preview.targetPlaceholder') }}</dd>
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
  background: var(--color-bg);
}
.route-preview div { display: grid; grid-template-columns: 7rem 1fr; gap: 0.75rem; }
.route-preview dt { color: var(--color-muted-strong); font-weight: 600; }
.route-preview dd { margin: 0; overflow-wrap: anywhere; font-family: monospace; }
</style>
