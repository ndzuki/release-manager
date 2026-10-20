<script setup lang="ts">
import { t } from '@/i18n/messages';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ref } from 'vue';
import { ActorKind, type AuditEvent } from '@/gen/audit/v1/audit_pb';
import CopyableHash from '@/components/common/CopyableHash.vue';
import Pagination from '@/components/common/Pagination.vue';
import StatusTag from '@/components/common/StatusTag.vue';
import { formatTimestamp } from '@/utils/formatTimestamp';
import { auditStatusLabel, auditStatusTone } from './auditEventStatus';

const props = defineProps<{
  events: AuditEvent[];
  totalSize: number;
  loading: boolean;
  hasPrevious: boolean;
  hasMore: boolean;
  /** 1-based page index, rendered and announced by the pager (TASK-375). */
  page: number;
}>();

const emit = defineEmits<{
  select: [event: AuditEvent];
  previous: [];
  next: [];
}>();

/*
 * TASK-375: paging must not strand focus on <body>. The pager deliberately never
 * touches focus (Pagination.vue documents that), so the caller owns this move.
 * A cursor page can disable the button that was just used ("next" on the last
 * page), and a browser blurs a focused control the moment it becomes disabled —
 * focus lands on <body> and a keyboard/screen-reader user loses their place.
 * The consumer calls this AFTER a page settled; the region carries an accessible
 * name (the section's aria-label), so the move is also announced.
 */
const resultsRef = ref<HTMLElement | null>(null);

function focusResults(): void {
  resultsRef.value?.focus();
}

defineExpose({ focusResults });

const actorKindLabels: Record<ActorKind, string> = {
  [ActorKind.UNSPECIFIED]: t('audit.table.unknownActor'),
  [ActorKind.ANONYMOUS]: t('audit.actor.anonymous'),
  [ActorKind.USER]: t('audit.actor.user'),
  [ActorKind.SERVICE]: t('audit.actor.service'),
  [ActorKind.API_KEY]: t('audit.actor.apiKey'),
  [ActorKind.SYSTEM]: t('audit.actor.service'),
};

function formatTime(event: AuditEvent): string {
  return event.createdAt ? formatTimestamp(timestampDate(event.createdAt), { style: 'medium' }) : '—';
}

function actorLabel(event: AuditEvent): string {
  const actor = event.actor;
  if (!actor) return 'unknown';
  return `${actorKindLabels[actor.kind]}:${actor.id || t('audit.table.unknownActor')}`;
}

/*
 * TASK-394 (direction C): the time column shows the clock only (HH:MM:SS), monospaced
 * so events can be compared down the column, and the FULL timestamp lives in `title`.
 * `timeStyle: 'medium'` is the same seconds precision formatTimestamp uses; dropping
 * the date here is a layout choice, not a precision change.
 */
const clockFormatter = new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium' });

function formatClock(event: AuditEvent): string {
  return event.createdAt ? clockFormatter.format(timestampDate(event.createdAt)) : '—';
}

function formatTimeTitle(event: AuditEvent): string | undefined {
  return event.createdAt ? formatTime(event) : undefined;
}
</script>

<template>
  <!-- tabindex="-1": a programmatic focus target only; it does not add a tab stop.
       The section already carries an aria-label, so a screen reader announces the
       region by name when the consumer moves focus here after a page change. -->
  <section ref="resultsRef" class="audit-results" tabindex="-1" :aria-label="t('audit.table.results')">
    <p class="audit-results__count">{{ t('audit.table.showing') }} {{ props.events.length }} / {{ props.totalSize }}</p>
    <div
      class="table-wrap audit-results__table-wrap"
      role="region"
      :aria-label="t('audit.table.scrollRegion')"
      tabindex="0"
    >
      <table>
        <!-- UX-017 (W14): a table without a caption has no accessible name, and
             a header cell without scope makes screen readers guess the axis. -->
        <caption class="visually-hidden">{{ t('audit.table.results') }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t('audit.table.time') }}</th>
            <th scope="col">{{ t('audit.table.actor') }}</th>
            <th scope="col">{{ t('audit.table.action') }}</th>
            <th scope="col">{{ t('audit.table.resource') }}</th>
            <th scope="col">{{ t('audit.table.status') }}</th>
            <th scope="col">{{ t('audit.table.duration') }}</th>
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
            @keydown.space.prevent="emit('select', event)"
          >
            <td>
              <time class="audit-results__time mono" :title="formatTimeTitle(event)">{{ formatClock(event) }}</time>
            </td>
            <td>
              <strong>{{ actorLabel(event) }}</strong>
            </td>
            <td>
              {{ event.action || '—' }}
            </td>
            <!-- TASK-394: the resource id is a machine identifier. It used to be printed
                 whole (a 36-character UUID folded into two lines and widened the table);
                 CopyableHash truncates it while keeping the full value in `title` and in
                 the copy button. `20 + 4` characters are still visible, so a short id
                 such as `operation-p2-0` renders in full and stays comparable in place.
                 The wrapper stops click/keydown from bubbling: pressing "copy" must not
                 also select the row and open the detail panel. -->
            <td>
              <span class="audit-results__resource">
                <span class="audit-results__resource-type">{{ event.resourceType || '—' }}</span>
                <span class="audit-results__resource-id" @click.stop @keydown.stop>
                  <CopyableHash v-if="event.resourceId" :value="event.resourceId" :head="20" :tail="4" />
                  <template v-else>—</template>
                </span>
              </span>
            </td>
            <td>
              <StatusTag
                :tone="auditStatusTone(event.status)"
                :label="auditStatusLabel(event.status) || '—'"
              />
            </td>
            <td class="audit-results__duration">{{ event.durationMs }} ms</td>
          </tr>
        </tbody>
      </table>
    </div>
    <!-- The shared pager owns the controls, the wording and the region's name
         (TASK-300). Two things stay this table's: the reachability it computes in
         props, and the event names its consumer binds. The pager cannot know a page
         is in flight, so `loading` is folded into reachability here; and the
         primitive emits `prev`/`next` while AuditPage listens for `previous`/`next`,
         so both directions are bridged explicitly. -->
    <Pagination
      class="audit-results__pagination"
      :has-prev="props.hasPrevious && !props.loading"
      :has-next="props.hasMore && !props.loading"
      :page="props.page"
      :total="props.totalSize"
      :label="t('audit.table.pagination')"
      @prev="emit('previous')"
      @next="emit('next')"
    />
  </section>
</template>

<style scoped>
.audit-results {
  display: grid;
  gap: 12px;
  min-width: 0;
}

/*
 * The region is a programmatic focus target (TASK-375). It is never a tab stop, so
 * the ring only appears after a page change moved focus here — that is exactly when
 * a sighted keyboard user needs to see where the focus went.
 */
.audit-results:focus {
  outline: 2px solid var(--color-primary);
  outline-offset: 0.25rem;
  border-radius: var(--r);
}

.audit-results__count {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

/* Direction C table frame: the surface is `--sf`, 2% lighter than the page, so the
   strong border is what separates it from the background (the plain `--bd` hairline
   measured ~1.3:1 and made the table read as a flat sheet). The `.table-wrap` base
   class supplies `overflow-x: auto; min-width: 0; max-width: 100%`. */
.audit-results__table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-sm);
}

th,
td {
  padding: 10px 20px;
  border-bottom: 1px solid var(--color-border-strong);
  text-align: left;
  vertical-align: middle;
  white-space: nowrap;
}

th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

tbody tr:last-child td {
  border-bottom: none;
}

.audit-results__time {
  color: var(--color-text-secondary);
}

.audit-results__resource {
  display: inline-flex;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.audit-results__resource-type {
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.audit-results__resource-id {
  min-width: 0;
}

.audit-results__duration {
  color: var(--mu);
  font-variant-numeric: tabular-nums;
}

.audit-results__row {
  cursor: pointer;
}

/*
 * The rows are tab stops (tabindex="0"), so keyboard focus must be visible.
 *
 * These rules used to share one block that ended in `outline: none`. A scoped
 * `.audit-results__row:focus-visible` (0,3,0 once scoped) beats base.css's global `:focus-visible`
 * (0,1,0), so that declaration silently cancelled the console's focus ring and left a
 * pale background tint as the only signal. Mouse hover and keyboard focus are split
 * again here, and the ring is drawn from the shared tokens, inset because the row
 * spans the table and an outside offset would be clipped by the rounded wrapper.
 */
.audit-results__row:hover {
  background: var(--sf2);
}

.audit-results__row:focus-visible {
  background: var(--sf2);
  outline: 2px solid var(--color-primary);
  outline-offset: -2px;
}

/*
 * The pager's look — button padding, border, radius and the disabled colour — moved
 * into the shared Pagination primitive (TASK-300). This rule keeps the one property
 * the page still owns: the row was right-aligned before the switch and stays that
 * way. The class reaches the primitive's root `<nav>` through attribute fallthrough,
 * and scoped CSS applies to a child's root node, so no wrapper is needed.
 */
.audit-results__pagination {
  justify-content: flex-end;
}
</style>
