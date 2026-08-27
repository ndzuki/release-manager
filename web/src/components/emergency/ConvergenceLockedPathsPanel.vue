<script setup lang="ts">
// Convergence locked-paths overlay (plan v3 Step 8): read-only presentation
// of the server-generated locked paths and bound task ids. The overlay is not
// a security boundary — Approve re-verifies every locked path in the
// transaction (AC-058-41).
defineProps<{
  lockedPaths: string[];
  taskIds: string[];
}>();
</script>

<template>
  <section class="locked-paths" aria-label="收敛锁定路径">
    <h3>收敛锁定路径（只读）</h3>
    <p class="hint">以下路径由收敛任务锁定，审批时服务端会逐项复核；编辑时请勿改动对应值。</p>
    <ul class="path-list">
      <li v-for="path in lockedPaths" :key="path"><code>{{ path }}</code></li>
    </ul>
    <p class="hint">绑定任务：{{ taskIds.join(', ') || '—' }}</p>
  </section>
</template>

<style scoped>
.locked-paths { display: grid; gap: 0.6rem; padding: 1rem; border: 1px solid #ddd6fe; border-radius: 0.65rem; background: #f5f3ff; }
.locked-paths h3 { margin: 0; color: #5b21b6; }
.path-list { display: grid; gap: 0.3rem; margin: 0; padding-left: 1.2rem; }
.path-list code { color: #334155; font-size: 0.8rem; }
.hint { margin: 0; color: #6d28d9; font-size: 0.8rem; }
</style>
