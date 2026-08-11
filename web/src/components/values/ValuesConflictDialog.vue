<script setup lang="ts">
defineProps<{ loading?: boolean }>();
const emit = defineEmits<{ reload: []; close: [] }>();
</script>

<template>
  <div class="dialog-backdrop" role="presentation" @click.self="emit('close')">
    <section class="dialog" role="dialog" aria-modal="true" aria-labelledby="values-conflict-title">
      <h2 id="values-conflict-title">Revision 已被更新</h2>
      <p>请重新基于最新 approved revision 计算 diff。当前编辑内容会保留，不会覆盖本地 draft。</p>
      <div class="dialog__actions">
        <button type="button" @click="emit('close')">稍后处理</button>
        <button type="button" class="primary" :disabled="loading" @click="emit('reload')">
          {{ loading ? '重新加载中…' : '重新加载最新 Revision' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.dialog-backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: #0f172a99; }
.dialog { display: grid; width: min(30rem, 100%); gap: 1rem; padding: 1.25rem; border-radius: 0.75rem; background: #fff; box-shadow: 0 1.5rem 4rem #0f172a55; }
h2, p { margin: 0; }
p { color: #475569; line-height: 1.6; }
.dialog__actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
button { min-height: 2.5rem; padding: 0.45rem 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; cursor: pointer; }
button.primary { border-color: #2563eb; background: #2563eb; color: #fff; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
</style>
