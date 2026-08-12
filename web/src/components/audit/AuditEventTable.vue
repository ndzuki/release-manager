<script setup lang="ts">
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ActorKind, type AuditEvent } from '@/gen/audit/v1/audit_pb';

const props = defineProps<{
  events: AuditEvent[];
  totalSize: number;
  loading: boolean;
  hasPrevious: boolean;
  hasMore: boolean;
}>();

const emit = defineEmits<{
  select: [event: AuditEvent];
  previous: [];
  next: [];
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

function actorLabel(event: AuditEvent): string {
  const actor = event.actor;
  if (!actor) return 'unknown';
  return `${actorKindLabels[actor.kind]}:${actor.id || 'unknown'}`;
}
</script>

<template>
  <section class="audit-results" aria-label="Audit results">
    <p class="audit-results__count">Showing {{ props.events.length }} of {{ props.totalSize }} events</p>
    <div class="audit-results__table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Actor</th>
            <th>Resource</th>
            <th>Action</th>
            <th>Status</th>
            <th>Duration</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="event in props.events"
            :key="event.id"
            class="audit-results__row"
            tabindex="0"
            @click="emit('select', event)"
            @keydown.enter="emit('select', event)"
          >
            <td>{{ formatTime(event) }}</td>
            <td>
              <strong>{{ actorLabel(event) }}</strong>
            </td>
            <td>
              <strong>{{ event.resourceType || '—' }}</strong>
              <small>{{ event.resourceId || '—' }}</small>
            </td>
            <td>
              {{ event.action || '—' }}
            </td>
            <td><span class="audit-status">{{ event.status || '—' }}</span></td>
            <td>{{ event.durationMs }} ms</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="audit-results__pagination" aria-label="Audit pagination">
      <button type="button" :disabled="!props.hasPrevious || props.loading" @click="emit('previous')">Previous</button>
      <button type="button" :disabled="!props.hasMore || props.loading" @click="emit('next')">Next</button>
    </div>
  </section>
</template>

<style scoped>
.audit-results {
  display: grid;
  gap: 1rem;
}

.audit-results__count {
  margin: 0;
  color: var(--color-muted, #64748b);
  font-size: 0.85rem;
}

.audit-results__table-wrap {
  overflow-x: auto;
  border: 1px solid var(--color-border, #e2e8f0);
  border-radius: 0.75rem;
  background: var(--color-surface, #fff);
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

th,
td {
  padding: 0.75rem;
  border-bottom: 1px solid #e2e8f0;
  text-align: left;
  vertical-align: top;
}

th {
  background: #f8fafc;
  color: #475569;
  font-size: 0.75rem;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

td small {
  display: block;
  margin-top: 0.2rem;
  color: var(--color-muted, #64748b);
}

.audit-results__row {
  cursor: pointer;
}

.audit-results__row:hover,
.audit-results__row:focus-visible {
  background: #eff6ff;
  outline: none;
}

.audit-status {
  display: inline-block;
  padding: 0.15rem 0.45rem;
  border-radius: 999px;
  background: #e2e8f0;
}

.audit-results__pagination {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
}

.audit-results__pagination button {
  padding: 0.5rem 0.8rem;
  border: 1px solid #cbd5e1;
  border-radius: 0.375rem;
  background: #fff;
  color: #334155;
  cursor: pointer;
}

.audit-results__pagination button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
</style>
