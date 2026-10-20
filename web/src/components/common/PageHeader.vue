<script setup lang="ts">
import { computed } from 'vue';
import type { RouteLocationRaw } from 'vue-router';
import { t } from '@/i18n/messages';

/*
 * Page heading: breadcrumbs, title and an optional subtitle, with an optional actions
 * slot. The shell no longer renders a title of its own, so this is the single place a
 * page states where the reader is.
 *
 * The crumbs carry NAMES supplied by the caller. That is the whole point: a breadcrumb
 * built from route params would print a UUID, which is unreadable and is exactly the
 * anti-pattern the direction-C spec calls out. The last crumb is the current page and is
 * therefore never a link, even if the caller passes `to` for it; `aria-current="page"`
 * marks it for assistive technology.
 */
const props = defineProps<{
  title: string;
  subtitle?: string;
  /*
   * Category label above the title ("发布 / 治理 / 紧急 / 维护"). A prop rather than a slot
   * because 18 pages already carry this in a hand-rolled `.eyebrow` element; moving them
   * onto PageHeader without it would silently drop the category.
   */
  eyebrow?: string;
  crumbs?: Array<{ label: string; to?: RouteLocationRaw }>;
}>();

// `crumbs` is optional; the template reads through this computed so `v-if` does not have
// to guard every access.
const crumbs = computed(() => props.crumbs ?? []);
</script>

<template>
  <header class="page-header">
    <nav v-if="crumbs.length > 0" class="page-header__crumbs" :aria-label="t('common.breadcrumb')">
      <ol class="page-header__crumb-list">
        <li v-for="(crumb, index) in crumbs" :key="`${index}-${crumb.label}`" class="page-header__crumb">
          <RouterLink v-if="crumb.to && index < crumbs.length - 1" class="page-header__crumb-link" :to="crumb.to">
            {{ crumb.label }}
          </RouterLink>
          <span v-else :aria-current="index === crumbs.length - 1 ? 'page' : undefined">{{ crumb.label }}</span>
          <span v-if="index < crumbs.length - 1" class="page-header__separator" aria-hidden="true">/</span>
        </li>
      </ol>
    </nav>

    <div class="page-header__heading">
      <div class="page-header__text">
        <p v-if="eyebrow" class="page-header__eyebrow">{{ eyebrow }}</p>
        <h1 class="page-header__title">{{ title }}</h1>
        <p v-if="subtitle" class="page-header__subtitle">{{ subtitle }}</p>
      </div>
      <div v-if="$slots.actions" class="page-header__actions">
        <slot name="actions" />
      </div>
    </div>
  </header>
</template>

<style scoped>
.page-header {
  display: grid;
  gap: 8px;
  margin-bottom: 20px;
}

.page-header__crumb-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  margin: 0;
  padding: 0;
  list-style: none;
}

.page-header__crumb {
  display: flex;
  gap: 6px;
  align-items: center;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.page-header__crumb-link {
  color: var(--mu);
  text-decoration: none;
}

.page-header__crumb-link:hover {
  color: var(--tx);
  text-decoration: underline;
}

.page-header__separator {
  /* --bd is a hairline border colour and disappears at 12px next to text; the muted
     text colour keeps the trail readable in both themes without competing with it. */
  color: var(--mu);
}

.page-header__heading {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-start;
  justify-content: space-between;
}

.page-header__text {
  display: grid;
  gap: 4px;
  min-width: 0;
}

/*
 * Direction-C eyebrow: 12px, semibold, muted, slightly tracked. The hand-rolled pages this
 * replaces used 700 and 800 in different files, which is the kind of drift a shared
 * component exists to remove.
 */
.page-header__eyebrow {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
}

.page-header__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-h1);
  font-weight: var(--font-weight-heading);
  letter-spacing: -0.01em;
  line-height: var(--line-height-tight);
}

.page-header__subtitle {
  margin: 0;
  max-width: 72ch;
  color: var(--mu);
  font-size: var(--font-size-md);
}

.page-header__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
</style>
