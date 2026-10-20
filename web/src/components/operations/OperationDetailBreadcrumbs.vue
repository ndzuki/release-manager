<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { ReleaseScope } from '@/composables/useOperationReleaseScope';

/*
 * The detail page's breadcrumbs. The release-scoped links exist only when the page
 * holds a release context (the canonical nested route, or one recovered from the
 * operation's release definition, TASK-279); otherwise the trail anchors on the
 * Operation centre so it never builds links from params the route does not have.
 */
defineProps<{
  releaseScope: ReleaseScope | null;
  releaseName: string;
  operationId: string;
}>();
</script>

<template>
  <nav class="operation-detail__breadcrumbs" :aria-label="t('values.page.breadcrumb')">
    <template v-if="releaseScope">
      <RouterLink
        :to="{
          name: 'ReleaseInventory',
          params: { customerId: releaseScope.customerId, clusterId: releaseScope.clusterId },
        }"
      >
        {{ t('release.inventory.breadcrumb') }}
      </RouterLink>
      <span aria-hidden="true">/</span>
      <RouterLink
        :to="{
          name: 'OperationList',
          params: {
            customerId: releaseScope.customerId,
            clusterId: releaseScope.clusterId,
            releaseId: releaseScope.releaseId,
          },
        }"
      >
        操作历史
      </RouterLink>
      <span aria-hidden="true">/</span>
    </template>
    <template v-else>
      <RouterLink :to="{ name: 'OperationCenter' }">{{ t('operationCenter.title') }}</RouterLink>
      <span aria-hidden="true">/</span>
    </template>
    <span>{{ releaseName }}</span><span aria-hidden="true">/</span>
    <strong>{{ operationId }}</strong>
  </nav>
</template>

<style scoped>
.operation-detail__breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: var(--color-muted); font-size: var(--font-size-sm); }
.operation-detail__breadcrumbs a { color: var(--color-primary); }
</style>
