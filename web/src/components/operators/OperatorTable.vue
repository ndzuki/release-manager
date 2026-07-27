<script setup lang="ts">
import OperatorStatusBadge from './OperatorStatusBadge.vue';
import type { OperatorSummary } from '@/types/operator';
import { formatOperatorTime, operatorSessionReasonLabel } from '@/utils/operator-format';

interface Props {
  operators: OperatorSummary[];
  canRevoke: boolean;
}

defineProps<Props>();
const emit = defineEmits<{
  open: [operatorId: string];
  revoke: [operator: OperatorSummary];
}>();
</script>

<template>
  <div class="table-wrap">
    <table class="operators">
      <thead>
        <tr><th>Name</th><th>Lifecycle</th><th>Session</th><th>Last heartbeat</th><th>Registered</th><th>Actions</th></tr>
      </thead>
      <tbody>
        <tr v-for="operator in operators" :key="operator.id">
          <td><button class="link" type="button" @click="emit('open', operator.id)">{{ operator.name || operator.id }}</button></td>
          <td><OperatorStatusBadge :lifecycle-status="operator.lifecycleStatus" /></td>
          <td>
            <OperatorStatusBadge :session-status="operator.sessionStatus" />
            <small v-if="operatorSessionReasonLabel(operator.sessionStatusReason)">{{ operatorSessionReasonLabel(operator.sessionStatusReason) }}</small>
          </td>
          <td>{{ formatOperatorTime(operator.lastHeartbeat) }}</td>
          <td>{{ formatOperatorTime(operator.registeredAt) }}</td>
          <td><button v-if="canRevoke && operator.lifecycleStatus !== 'revoked'" type="button" class="danger" @click="emit('revoke', operator)">Revoke</button></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.table-wrap { overflow-x: auto; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.operators { width: 100%; border-collapse: collapse; }
.operators th, .operators td { padding: 0.8rem; border-bottom: 1px solid #e2e8f0; text-align: left; vertical-align: top; white-space: nowrap; }
.operators th { background: #f8fafc; color: #475569; font-size: 0.8rem; text-transform: uppercase; }
.operators small { display: block; max-width: 18rem; margin-top: 0.35rem; color: #64748b; white-space: normal; }
.link { border: 0; background: transparent; color: #2563eb; cursor: pointer; font-weight: 700; }
.danger { padding: 0.35rem 0.6rem; border: 1px solid #ef4444; border-radius: 0.375rem; background: #fff; color: #b91c1c; cursor: pointer; }
</style>
