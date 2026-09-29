<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ActorKind, type AuditEvent } from '@/gen/audit/v1/audit_pb';

const props = defineProps<{
  event: AuditEvent;
}>();

const emit = defineEmits<{
  close: [];
}>();

const actorKindLabels: Record<ActorKind, string> = {
  [ActorKind.UNSPECIFIED]: t('audit.table.unknownActor'),
  [ActorKind.ANONYMOUS]: t('audit.actor.anonymous'),
  [ActorKind.USER]: t('audit.actor.user'),
  [ActorKind.SERVICE]: t('audit.actor.service'),
  [ActorKind.API_KEY]: t('audit.actor.apiKey'),
  [ActorKind.SYSTEM]: t('audit.actor.service'),
};

function formatTime(event: AuditEvent): string {
  return event.createdAt ? timestampDate(event.createdAt).toLocaleString() : '—';
}
</script>

<template>
  <aside class="audit-detail" :aria-label="t('audit.detail.title')">
    <header class="audit-detail__header">
      <div>
        <p>{{ t('audit.detail.title') }}</p>
        <h2>{{ props.event.id }}</h2>
      </div>
      <button type="button" :aria-label="t('audit.detail.close')" @click="emit('close')">{{ t('action.close') }}</button>
    </header>
    <dl class="audit-detail__grid">
      <div>
        <dt>{{ t('audit.detail.timestamp') }}</dt>
        <dd>{{ formatTime(props.event) }}</dd>
      </div>
      <div>
        <dt>{{ t('audit.table.actor') }}</dt>
        <dd>{{ actorKindLabels[props.event.actor?.kind ?? ActorKind.UNSPECIFIED] }}:{{ props.event.actor?.id || t('audit.table.unknownActor') }}</dd>
      </div>
      <div>
        <dt>{{ t('audit.table.resource') }}</dt>
        <dd>{{ props.event.resourceType }}:{{ props.event.resourceId }}</dd>
      </div>
      <div>
        <dt>{{ t('audit.table.action') }}</dt>
        <dd>{{ props.event.action }}</dd>
      </div>
      <div>
        <dt>{{ t('audit.table.status') }}</dt>
        <dd>{{ statusLabel('audit', props.event.status) }}</dd>
      </div>
      <div>
        <dt>{{ t('audit.table.duration') }}</dt>
        <dd>{{ props.event.durationMs }} ms</dd>
      </div>
      <div class="audit-detail__summary">
        <dt>{{ t('audit.detail.changeSummary') }}</dt>
        <dd>{{ props.event.changeSummary || '—' }}</dd>
      </div>
    </dl>
  </aside>
</template>

<style scoped>
.audit-detail {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-info-border-soft);
  border-radius: 0.75rem;
  background: var(--color-info-soft);
}

.audit-detail__header {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 1rem;
}

.audit-detail__header p,
.audit-detail__header h2 {
  margin: 0;
}

.audit-detail__header p {
  color: var(--color-primary-hover);
  font-size: var(--font-size-xs);
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.audit-detail__header h2 {
  font-family: ui-monospace, monospace;
  font-size: var(--font-size-base);
}

.audit-detail__header button {
  padding: 0.4rem 0.65rem;
  border: 1px solid var(--color-info-border);
  border-radius: 0.375rem;
  background: var(--color-surface);
  color: var(--color-primary-hover);
  cursor: pointer;
}

.audit-detail__grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
  gap: 1rem;
  margin: 0;
}

.audit-detail__grid div {
  min-width: 0;
}

.audit-detail__grid dt {
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 700;
  text-transform: uppercase;
}

.audit-detail__grid dd {
  margin: 0.25rem 0 0;
  overflow-wrap: anywhere;
}

.audit-detail__summary {
  grid-column: 1 / -1;
}
</style>
