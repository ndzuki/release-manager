<script setup lang="ts">
import AppDialog from '@/components/common/AppDialog.vue';

defineProps<{ loading?: boolean }>();
const emit = defineEmits<{ reload: []; close: [] }>();
</script>

<template>
  <AppDialog
    :open="true"
    title="Revision 已被更新"
    description="请重新基于最新 approved revision 计算 diff。当前编辑内容会保留，不会覆盖本地 draft。"
    @close="emit('close')"
  >
    <template #footer>
      <button type="button" @click="emit('close')">稍后处理</button>
      <button type="button" class="primary" :disabled="loading" @click="emit('reload')">
        {{ loading ? '重新加载中…' : '重新加载最新 Revision' }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.primary {
  border-color: var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-accent);
}
</style>
