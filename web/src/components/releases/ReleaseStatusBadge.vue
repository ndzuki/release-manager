<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import type { ReleaseStatus } from '@/stores/releaseInventory';

const props = defineProps<{ status: ReleaseStatus }>();

const details = computed(() => {
  switch (props.status) {
    case 'missing':
      return { label: t('release.status.missing'), icon: '!', tooltip: 'Release 已从集群中消失' };
    case 'out_of_sync':
      return { label: t('release.status.outOfSync'), icon: '↯', tooltip: '配置与期望不一致' };
    default:
      return { label: t('release.status.active'), icon: '✓', tooltip: 'Release 与最近一次同步结果一致' };
  }
});
</script>

<template>
  <span class="release-status" :class="`release-status--${status}`" :title="details.tooltip">
    <span aria-hidden="true">{{ details.icon }}</span>
    <span>{{ details.label }}</span>
  </span>
</template>

<style scoped>
.release-status {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.25rem 0.55rem;
  border: 1px solid currentColor;
  border-radius: 999px;
  font-size: var(--font-size-xs);
  font-weight: 700;
}
.release-status--active { color: var(--color-success-ink); background: var(--color-success-surface-soft); }
.release-status--missing { color: var(--color-error-strong); background: var(--color-danger-soft); }
.release-status--out_of_sync { color: var(--color-warning-ink); background: var(--color-warning-soft); }
</style>
