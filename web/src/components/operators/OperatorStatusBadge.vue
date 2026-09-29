<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import type { OperatorLifecycleStatus, OperatorSessionStatus } from '@/types/operator';

interface Props {
  lifecycleStatus?: OperatorLifecycleStatus;
  sessionStatus?: OperatorSessionStatus;
}

const props = defineProps<Props>();
const value = computed(() => props.sessionStatus ?? props.lifecycleStatus ?? 'none');

/*
 * The badge used to print the server enum verbatim (`Active`, `Online`), so the table
 * disagreed with the filter labels that already had translations. Unknown values still
 * fall back to the raw value: a new server status must remain visible rather than blank.
 */
const LABEL_KEYS = {
  active: 'operator.lifecycle.active',
  superseded: 'operator.lifecycle.superseded',
  revoked: 'operator.lifecycle.revoked',
  none: 'operator.filters.noSession',
  online: 'operator.session.online',
  suspect: 'operator.session.suspect',
  offline: 'operator.session.offline',
  // A declared enum value, not a surprise value: it deserves a translation too.
  unknown: 'operator.status.unknown',
} as const;

const label = computed(() => {
  const key = LABEL_KEYS[value.value as keyof typeof LABEL_KEYS];
  return key ? t(key) : value.value.replaceAll('_', ' ');
});
</script>

<template>
  <span class="status" :class="`status--${value}`">{{ label }}</span>
</template>

<style scoped>
.status { display: inline-flex; padding: 0.2rem 0.55rem; border-radius: 999px; background: var(--color-border); color: var(--color-text-secondary); font-size: var(--font-size-xs); font-weight: 700; text-transform: capitalize; }
.status--online, .status--active { background: var(--color-success-surface); color: var(--color-success-ink); }
.status--suspect { background: var(--color-warning-subtle); color: var(--color-warning-ink-strong); }
.status--offline, .status--superseded { background: var(--color-border); color: var(--color-muted-strong); }
.status--revoked { background: var(--color-danger-surface); color: var(--color-error-strong); }
.status--unknown { background: var(--color-violet-subtle); color: var(--color-violet-ink); }
</style>
