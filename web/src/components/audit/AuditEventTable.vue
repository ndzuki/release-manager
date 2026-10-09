<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
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

function actorLabel(event: AuditEvent): string {
  const actor = event.actor;
  if (!actor) return 'unknown';
  return `${actorKindLabels[actor.kind]}:${actor.id || t('audit.table.unknownActor')}`;
}
</script>

<template>
  <section class="audit-results" :aria-label="t('audit.table.results')">
    <p class="audit-results__count">{{ t('audit.table.showing') }} {{ props.events.length }} / {{ props.totalSize }}</p>
    <div class="audit-results__table-wrap">
      <table>
        <thead>
          <tr>
            <th>{{ t('audit.table.time') }}</th>
            <th>{{ t('audit.table.actor') }}</th>
            <th>{{ t('audit.table.resource') }}</th>
            <th>{{ t('audit.table.action') }}</th>
            <th>{{ t('audit.table.status') }}</th>
            <th>{{ t('audit.table.duration') }}</th>
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
            <td><span class="audit-status">{{ statusLabel('audit', event.status) || '—' }}</span></td>
            <td>{{ event.durationMs }} ms</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="audit-results__pagination" :aria-label="t('audit.table.pagination')">
      <button type="button" :disabled="!props.hasPrevious || props.loading" @click="emit('previous')">{{ t('audit.table.previous') }}</button>
      <button type="button" :disabled="!props.hasMore || props.loading" @click="emit('next')">{{ t('audit.table.next') }}</button>
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
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.audit-results__table-wrap {
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: 0.75rem;
  background: var(--color-surface);
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-sm);
}

th,
td {
  padding: 0.75rem;
  border-bottom: 1px solid var(--color-border);
  text-align: left;
  vertical-align: top;
}

th {
  background: var(--color-bg);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

td small {
  display: block;
  margin-top: 0.2rem;
  color: var(--color-muted);
}

.audit-results__row {
  cursor: pointer;
}

/*
 * The rows are tab stops (tabindex="0"), so keyboard focus must be visible.
 *
 * These rules used to share one block that ended in `outline: none`. A scoped
 * `.audit-results__row:focus-visible` (0,2,0) beats base.css's global `:focus-visible`
 * (0,1,0), so that declaration silently cancelled the console's focus ring and left a
 * pale background tint as the only signal. Mouse hover and keyboard focus are split
 * again here, and the ring is drawn from the shared tokens, inset because the row
 * spans the table and an outside offset would be clipped by the rounded wrapper.
 */
.audit-results__row:hover {
  background: var(--color-info-soft);
}

.audit-results__row:focus-visible {
  background: var(--color-info-soft);
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}

.audit-status {
  display: inline-block;
  padding: 0.15rem 0.45rem;
  border-radius: 999px;
  background: var(--color-border);
}

.audit-results__pagination {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
}

.audit-results__pagination button {
  padding: 0.5rem 0.8rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
  background: var(--color-surface);
  color: var(--color-text-secondary);
  cursor: pointer;
}

.audit-results__pagination button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
</style>
