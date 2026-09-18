<script setup lang="ts">
/**
 * REQ-033 AC-033-10: when the authorization snapshot is not fresh the write
 * entries are hidden or disabled, while the convergence paths stay readable.
 * The server remains the final authority (ADR-006 fail closed) — this notice
 * explains the UX state, it does not enforce anything.
 */
defineProps<{ stale: boolean }>();
</script>

<template>
  <p v-if="stale" class="auth-stale-notice" role="status">
    授权数据未同步：写入入口已暂时停用，只读内容仍可查看。服务端仍会拒绝越权写入。
  </p>
</template>

<style scoped>
.auth-stale-notice {
  margin: 0 0 1rem;
  padding: 0.65rem 0.85rem;
  border: 1px solid #fdba74;
  border-radius: 0.5rem;
  background: #fff7ed;
  color: #9a3412;
  font-size: 0.85rem;
}
</style>
