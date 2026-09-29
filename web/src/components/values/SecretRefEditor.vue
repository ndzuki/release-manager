<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import type { SecretOption, SecretRef, SecretRefFormItem } from '@/types/valuesRevision';
import { suggestedSecretPath } from '@/utils/valuesValidation';

const props = defineProps<{
  items: SecretRefFormItem[];
  secrets: SecretOption[];
  disabled?: boolean;
  error?: string | null;
}>();

const emit = defineEmits<{
  add: [];
  remove: [id: string];
  update: [id: string, patch: Partial<SecretRef>];
}>();

const optionsByName = computed(() => Object.fromEntries(props.secrets.map((secret) => [secret.name, secret])));

function keysFor(item: SecretRefFormItem): string[] {
  return optionsByName.value[item.name]?.keys ?? [];
}

function updateName(item: SecretRefFormItem, event: Event): void {
  const name = (event.target as HTMLSelectElement).value;
  emit('update', item.id, { name, key: '', path: '' });
}

function updateKey(item: SecretRefFormItem, event: Event): void {
  const key = (event.target as HTMLSelectElement).value;
  emit('update', item.id, { key, path: suggestedSecretPath(item.name, key) });
}
</script>

<template>
  <section class="secret-editor" aria-labelledby="secret-refs-title">
    <header class="secret-editor__header">
      <div>
        <p class="eyebrow">{{ t('values.secretRefs.title') }}</p>
        <h2 id="secret-refs-title">{{ t('values.secretRefs.kind') }}</h2>
      </div>
      <button type="button" :disabled="disabled" @click="emit('add')">添加 SecretRef</button>
    </header>

    <p class="secret-editor__help">仅保存 namespace 内 Secret 的 name、key 与目标 path，不读取或传输 value。</p>
    <p v-if="error" class="secret-editor__error" role="alert">{{ error }}</p>
    <p v-if="items.length === 0" class="secret-editor__empty">尚未配置 SecretRef。</p>

    <div v-for="(item, index) in items" :key="item.id" class="secret-row">
      <strong>SecretRef {{ index + 1 }}</strong>
      <label>
        {{ t('values.secretRefs.name') }}
        <select :value="item.name" :disabled="disabled" @change="updateName(item, $event)">
          <option value="">请选择 Secret 名称</option>
          <option v-for="secret in secrets" :key="secret.name" :value="secret.name">{{ secret.name }}</option>
        </select>
      </label>
      <label>
        {{ t('values.secretRefs.key') }}
        <select :value="item.key" :disabled="disabled || !item.name" @change="updateKey(item, $event)">
          <option value="">请选择 Key</option>
          <option v-for="key in keysFor(item)" :key="key" :value="key">{{ key }}</option>
        </select>
      </label>
      <label>
        {{ t('values.secretRefs.path') }}
        <input :value="item.path" readonly placeholder="选择 name/key 后自动生成" />
      </label>
      <button type="button" class="danger-text" :disabled="disabled" @click="emit('remove', item.id)">移除</button>
    </div>
  </section>
</template>

<style scoped>
.secret-editor { display: grid; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
.secret-editor__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.secret-editor__header h2, .secret-editor__header p, .secret-editor__help, .secret-editor__empty, .secret-editor__error { margin: 0; }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.secret-editor__help, .secret-editor__empty { color: var(--color-muted); font-size: var(--font-size-sm); }
.secret-editor__error { color: var(--color-error); font-size: var(--font-size-sm); }
.secret-row { display: grid; grid-template-columns: minmax(10rem, 1fr) minmax(10rem, 1fr) minmax(14rem, 1.4fr) auto; align-items: end; gap: 0.75rem; padding: 0.85rem; border: 1px solid var(--color-border); border-radius: 0.55rem; }
.secret-row > strong { grid-column: 1 / -1; }
label { display: grid; gap: 0.35rem; color: var(--color-muted-strong); font-size: var(--font-size-xs); font-weight: 700; }
button, select, input { min-height: 2.4rem; padding: 0.45rem 0.65rem; border: 1px solid var(--color-border-strong); border-radius: 0.4rem; background: var(--color-surface); }
button { cursor: pointer; }
button:disabled, select:disabled { cursor: not-allowed; opacity: 0.6; }
.danger-text { border-color: transparent; color: var(--color-error); }
@media (max-width: 56rem) { .secret-row { grid-template-columns: 1fr; } }
</style>
