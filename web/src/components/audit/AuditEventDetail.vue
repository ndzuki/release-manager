<script setup lang="ts">
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ActorKind, type AuditEvent } from '@/gen/audit/v1/audit_pb';

const props = defineProps<{
  event: AuditEvent;
}>();

const emit = defineEmits<{
  close: [];
}>();

const actorKindLabels: Record<ActorKind, string> = {
  [ActorKind.UNSPECIFIED]: 'unknown',
  [ActorKind.ANONYMOUS]: 'anonymous',
  [ActorKind.USER]: 'user',
  [ActorKind.SERVICE]: 'service',
  [ActorKind.API_KEY]: 'api_key',
  [ActorKind.SYSTEM]: 'system',
};

function formatTime(event: AuditEvent): string {
  return event.createdAt ? timestampDate(event.createdAt).toLocaleString() : '—';
}
</script>

<template>
  <aside class="audit-detail" aria-label="Audit event details">
    <header class="audit-detail__header">
      <div>
        <p>Audit event</p>
        <h2>{{ props.event.id }}</h2>
      </div>
      <button type="button" aria-label="Close audit details" @click="emit('close')">Close</button>
    </header>
    <dl class="audit-detail__grid">
      <div>
        <dt>Timestamp</dt>
        <dd>{{ formatTime(props.event) }}</dd>
      </div>
      <div>
        <dt>Actor</dt>
        <dd>{{ actorKindLabels[props.event.actor?.kind ?? ActorKind.UNSPECIFIED] }}:{{ props.event.actor?.id || 'unknown' }}</dd>
      </div>
      <div>
        <dt>Resource</dt>
        <dd>{{ props.event.resourceType }}:{{ props.event.resourceId }}</dd>
      </div>
      <div>
        <dt>Action</dt>
        <dd>{{ props.event.action }}</dd>
      </div>
      <div>
        <dt>Status</dt>
        <dd>{{ props.event.status }}</dd>
      </div>
      <div>
        <dt>Duration</dt>
        <dd>{{ props.event.durationMs }} ms</dd>
      </div>
      <div class="audit-detail__summary">
        <dt>Change summary</dt>
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
  border: 1px solid #bfdbfe;
  border-radius: 0.75rem;
  background: #eff6ff;
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
  color: #1d4ed8;
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.audit-detail__header h2 {
  font-family: ui-monospace, monospace;
  font-size: 1rem;
}

.audit-detail__header button {
  padding: 0.4rem 0.65rem;
  border: 1px solid #93c5fd;
  border-radius: 0.375rem;
  background: #fff;
  color: #1d4ed8;
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
  color: #475569;
  font-size: 0.75rem;
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
