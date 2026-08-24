<script setup lang="ts">
// Target-bound VERIFIED artifact selection (plan v3 Step 4, AC-058-10):
// artifacts come exclusively from the server candidate list — no free-form
// digest/repository input exists in this component.
import type { CandidateArtifactDisplay } from '@/features/emergency/model';

defineProps<{
  containers: string[];
  selectedContainer: string;
  artifacts: CandidateArtifactDisplay[];
  selectedArtifactId: string | null;
  loading: boolean;
  error: string | null;
}>();

const emit = defineEmits<{
  'select-container': [container: string];
  'select-artifact': [artifactId: string];
}>();

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '未知';
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

    <p v-if="loading" class="hint">正在加载候选制品…</p>
    <p v-else-if="error" class="hint error-text">{{ error }}</p>
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
          <span class="meta">验证于 {{ formatTimestamp(artifact.validatedAt) }}</span>
        </span>
      </label>
    </div>
  </div>
</template>

<style scoped>
.artifact-selector { display: grid; gap: 0.75rem; }
.field-label { display: block; margin-bottom: 0.25rem; color: #475569; font-size: 0.85rem; }
.field-input { width: 100%; padding: 0.5rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; }
.artifact-list { display: grid; gap: 0.5rem; }
.artifact-card { display: flex; gap: 0.75rem; align-items: flex-start; padding: 0.75rem; border: 1px solid #e2e8f0; border-radius: 0.5rem; background: #fff; cursor: pointer; }
.artifact-card.selected { border-color: #2563eb; background: #eff6ff; }
.artifact-body { display: grid; gap: 0.2rem; }
.digest { font-family: monospace; font-size: 0.8rem; color: #475569; }
.meta { font-size: 0.8rem; color: #94a3b8; }
.hint { color: #64748b; }
.error-text { color: #b91c1c; }
</style>
