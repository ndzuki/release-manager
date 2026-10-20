<script setup lang="ts">
import AppDialog from '@/components/common/AppDialog.vue';

defineProps<{ loading?: boolean }>();
const emit = defineEmits<{ reload: []; close: [] }>();
</script>

<template>
  <AppDialog
    :open="true"
    title="版本已被更新"
    description="请基于最新已批准的版本重新计算差异。当前编辑内容会保留，不会覆盖本地草稿。"
    @close="emit('close')"
  >
    <template #footer>
      <button type="button" @click="emit('close')">稍后处理</button>
      <button type="button" class="primary" :disabled="loading" @click="emit('reload')">
        {{ loading ? '重新加载中…' : '重新加载最新版本' }}
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
