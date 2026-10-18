<script setup lang="ts">
// Target-bound VERIFIED artifact selection (plan v3 Step 4, AC-058-10):
// artifacts come exclusively from the server candidate list — no free-form
// digest/repository input exists in this component.
import { computed } from 'vue';
import { t } from '@/i18n/messages';
import type { CandidateArtifactDisplay } from '@/features/emergency/model';
import { formatTimestamp } from '@/utils/formatTimestamp';

const props = defineProps<{
  containers: string[];
  selectedContainer: string;
  /**
   * The selected container's observed image ref, from the emergency read model
   * (imageActions[].currentImageRef). Missing means "not observed": the server
   * gates the image action on a fresh observation, so an empty value is a
   * fail-closed corner (e.g. a container listed without an image ref) and the
   * UI states it rather than fabricating a ref.
   */
  currentImageRef?: string;
  artifacts: CandidateArtifactDisplay[];
  selectedArtifactId: string | null;
  loading: boolean;
  error: string | null;
}>();

/** "当前镜像：<ref>", or the not-observed copy when the ref is empty. */
const currentImageText = computed(() => {
  const current = props.currentImageRef ?? '';
  return current === '' ? t('emergency.image.currentUnknown') : t('emergency.image.current', { ref: current });
});

const emit = defineEmits<{
  'select-container': [container: string];
  'select-artifact': [artifactId: string];
}>();


// W17: keep the selector's own fallback word, share the formatting.
function formatValidatedAt(value: string | null): string {
  return formatTimestamp(value, { fallback: '未知' });
}
</script>

<template>
  <div class="artifact-selector">
    <label>
      <span class="field-label">容器</span>
      <select
        class="field-input"
        :value="selectedContainer"
        @change="emit('select-container', ($event.target as HTMLSelectElement).value)"
      >
        <option value="" disabled>选择容器</option>
        <option v-for="container in containers" :key="container" :value="container">{{ container }}</option>
      </select>
    </label>

    <!-- The observed image of the container the operator just picked: the read
         model carries it per container (imageActions[].currentImageRef), and it
         was previously projected all the way to the store but never rendered. -->
    <p v-if="selectedContainer !== ''" class="hint current-image">{{ currentImageText }}</p>

    <p v-if="loading" class="hint">正在加载候选制品…</p>
    <p v-else-if="error" class="hint error-text">{{ error }}</p>
    <!--
      Until a container is chosen the list is empty for a reason that has nothing to do
      with verification: the server scopes candidates to that container's repository. Saying
      "no VERIFIED candidates" here was misleading (found while running the browser smoke:
      the API returned a verified artifact while the page claimed none existed).
    -->
    <div v-else-if="selectedContainer === ''" class="hint">请先选择容器，再挑选候选制品</div>
    <div v-else-if="artifacts.length === 0" class="hint">没有可用的 VERIFIED 候选制品</div>
    <div v-else class="artifact-list" role="radiogroup" aria-label="选择候选制品">
      <label
        v-for="artifact in artifacts"
        :key="artifact.id"
        class="artifact-card"
        :class="{ selected: artifact.id === selectedArtifactId }"
      >
        <input
          type="radio"
          name="emergency-artifact"
          :checked="artifact.id === selectedArtifactId"
          @change="emit('select-artifact', artifact.id)"
        />
        <span class="artifact-body">
          <strong>{{ artifact.repository }}</strong>
          <span class="digest">{{ artifact.digest }}</span>
          <span class="meta">验证于 {{ formatValidatedAt(artifact.validatedAt) }}</span>
        </span>
      </label>
    </div>
  </div>
</template>

<style scoped>
.artifact-selector { display: grid; gap: 0.75rem; }
.field-label { display: block; margin-bottom: 0.25rem; color: var(--color-muted-strong); font-size: var(--font-size-sm); }
.field-input { width: 100%; padding: 0.5rem; border: 1px solid var(--color-border-strong); border-radius: 0.375rem; }
.artifact-list { display: grid; gap: 0.5rem; }
.artifact-card { display: flex; gap: 0.75rem; align-items: flex-start; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 0.5rem; background: var(--color-surface); cursor: pointer; }
.artifact-card.selected { border-color: var(--color-primary); background: var(--color-info-soft); }
.artifact-body { display: grid; gap: 0.2rem; }
.digest { font-family: monospace; font-size: var(--font-size-sm); color: var(--color-muted-strong); }
.meta { font-size: var(--font-size-sm); color: var(--color-subtle); }
.hint { color: var(--color-muted); }
/* An image ref is one long token; without this it overflows the grid column. */
.current-image { overflow-wrap: anywhere; }
.error-text { color: var(--color-error); }
</style>
