<script setup lang="ts">
import { computed } from 'vue';
import type { OperatorLifecycleStatus, OperatorSessionStatus } from '@/types/operator';

interface Props {
  lifecycleStatus?: OperatorLifecycleStatus;
  sessionStatus?: OperatorSessionStatus;
}

const props = defineProps<Props>();
const value = computed(() => props.sessionStatus ?? props.lifecycleStatus ?? 'none');
const label = computed(() => value.value === 'none' ? 'No session' : value.value.replaceAll('_', ' '));
</script>

<template>
  <span class="status" :class="`status--${value}`">{{ label }}</span>
</template>

<style scoped>
.status { display: inline-flex; padding: 0.2rem 0.55rem; border-radius: 999px; background: #e2e8f0; color: #334155; font-size: 0.75rem; font-weight: 700; text-transform: capitalize; }
.status--online, .status--active { background: #dcfce7; color: #166534; }
.status--suspect { background: #fef3c7; color: #92400e; }
.status--offline, .status--superseded { background: #e2e8f0; color: #475569; }
.status--revoked { background: #fee2e2; color: #991b1b; }
.status--unknown { background: #ede9fe; color: #5b21b6; }
</style>
