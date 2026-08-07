<script setup lang="ts">
import { computed } from 'vue';
import type { ReleaseStatus } from '@/stores/releaseInventory';

const props = defineProps<{ status: ReleaseStatus }>();

const details = computed(() => {
  switch (props.status) {
    case 'missing':
      return { label: 'Missing', icon: '!', tooltip: 'Release 已从集群中消失' };
    case 'out_of_sync':
      return { label: 'Out of sync', icon: '↯', tooltip: '配置与期望不一致' };
    default:
      return { label: 'Active', icon: '✓', tooltip: 'Release 与最近一次同步结果一致' };
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
  font-size: 0.75rem;
  font-weight: 700;
}
.release-status--active { color: #166534; background: #f0fdf4; }
.release-status--missing { color: #991b1b; background: #fef2f2; }
.release-status--out_of_sync { color: #9a3412; background: #fff7ed; }
</style>
