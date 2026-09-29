<script setup lang="ts">
import { t } from '@/i18n/messages';
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
        <tr><th>{{ t('operator.table.name') }}</th><th>{{ t('operator.filters.lifecycle') }}</th><th>{{ t('operator.filters.session') }}</th><th>{{ t('operator.table.lastHeartbeat') }}</th><th>{{ t('operator.table.registered') }}</th><th>{{ t('operator.table.actions') }}</th></tr>
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
          <td><button v-if="canRevoke && operator.lifecycleStatus !== 'revoked'" type="button" class="danger" @click="emit('revoke', operator)">{{ t('operator.table.revoke') }}</button></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.table-wrap { overflow-x: auto; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.operators { width: 100%; border-collapse: collapse; }
.operators th, .operators td { padding: 0.8rem; border-bottom: 1px solid var(--color-border); text-align: left; vertical-align: top; white-space: nowrap; }
.operators th { background: var(--color-bg); color: var(--color-muted-strong); font-size: var(--font-size-sm); text-transform: uppercase; }
.operators small { display: block; max-width: 18rem; margin-top: 0.35rem; color: var(--color-muted); white-space: normal; }
.link { border: 0; background: transparent; color: var(--color-primary); cursor: pointer; font-weight: 700; }
.danger { padding: 0.35rem 0.6rem; border: 1px solid var(--color-danger-border); border-radius: 0.375rem; background: var(--color-surface); color: var(--color-error); cursor: pointer; }
</style>
