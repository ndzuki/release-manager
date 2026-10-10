<script setup lang="ts">
import { computed, reactive } from 'vue';
import { t } from '@/i18n/messages';
import FormField from '@/components/common/FormField.vue';
import { inspectPublicKeyPem, type TrustRootView } from '@/connect/trust-api';
import { useTrustPolicyStore } from '@/stores/trustPolicy';

/*
 * Create / rotate a signature trust root (TASK-280 D11).
 *
 * Material contract: the server takes only a PUBLIC key PEM
 * (api/proto/trust/v1/trust.proto:78,94; internal/trust/types.go:112-114 rejects a
 * PRIVATE block). This form therefore never generates a key pair — a browser-private
 * key would have to be exported through a non-encrypted path. The operator pastes the
 * public half produced by the signer's KMS/CI, and inspectPublicKeyPem refuses
 * anything that looks private before a request is built.
 */
const props = defineProps<{ mode: 'create' | 'rotate'; root?: TrustRootView | null }>();
const emit = defineEmits<{ close: [] }>();

const store = useTrustPolicyStore();
const DEFAULT_GRACE_HOURS = 24;
const form = reactive({
  keyId: '',
  issuer: '',
  subjectPattern: '',
  publicKeyPem: '',
  graceHours: DEFAULT_GRACE_HOURS,
});
const errors = reactive<{ keyId?: string; issuer?: string; publicKeyPem?: string; graceHours?: string }>({});
const submitting = computed(() => store.saving);

function close(): void {
  emit('close');
}

function validate(): boolean {
  Object.keys(errors).forEach((key) => delete errors[key as keyof typeof errors]);
  if (form.keyId.trim() === '') errors.keyId = t('trust.editor.keyIdRequired');
  if (form.issuer.trim() === '') errors.issuer = t('trust.editor.issuerRequired');
  const issue = inspectPublicKeyPem(form.publicKeyPem);
  if (issue === 'empty') errors.publicKeyPem = t('trust.editor.publicKeyRequired');
  else if (issue === 'private_key') errors.publicKeyPem = t('trust.editor.publicKeyPrivate');
  else if (issue === 'not_public_key') errors.publicKeyPem = t('trust.editor.publicKeyFormat');
  if (props.mode === 'rotate' && (!Number.isFinite(form.graceHours) || form.graceHours < 0)) {
    errors.graceHours = t('trust.editor.graceHoursInvalid');
  }
  return Object.keys(errors).length === 0;
}

async function submit(): Promise<void> {
  if (submitting.value || !validate()) return;
  const material = {
    keyId: form.keyId.trim(),
    issuer: form.issuer.trim(),
    subjectPattern: form.subjectPattern.trim(),
    publicKeyPem: form.publicKeyPem.trim(),
  };
  const ok =
    props.mode === 'create'
      ? await store.createRoot(material)
      : await store.rotateRoot({
          ...material,
          oldRootId: props.root?.id ?? '',
          graceUntil: new Date(Date.now() + form.graceHours * 60 * 60 * 1000),
        });
  if (ok) close();
}
</script>

<template>
  <form class="trust-editor" @submit.prevent="submit">
    <h2>
      {{ mode === 'create' ? t('trust.create.title') : t('trust.rotate.title', { keyId: root?.keyId ?? '' }) }}
    </h2>

    <FormField :label="t('trust.editor.keyId')" :error="errors.keyId" required>
      <template #default="{ id, describedBy, invalid, required, disabled }">
        <input
          :id="id"
          v-model="form.keyId"
          type="text"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :required="required"
          :disabled="disabled || submitting"
          data-testid="trust-editor-key-id"
        />
      </template>
    </FormField>

    <FormField :label="t('trust.editor.issuer')" :error="errors.issuer" required>
      <template #default="{ id, describedBy, invalid, required, disabled }">
        <input
          :id="id"
          v-model="form.issuer"
          type="text"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :required="required"
          :disabled="disabled || submitting"
          data-testid="trust-editor-issuer"
        />
      </template>
    </FormField>

    <FormField
      :label="t('trust.editor.subjectPattern')"
      :help="t('trust.editor.subjectPatternHint')"
    >
      <template #default="{ id, describedBy, disabled }">
        <input
          :id="id"
          v-model="form.subjectPattern"
          type="text"
          :aria-describedby="describedBy"
          :disabled="disabled || submitting"
          data-testid="trust-editor-subject-pattern"
        />
      </template>
    </FormField>

    <FormField
      :label="t('trust.editor.publicKey')"
      :help="t('trust.editor.publicKeyHelp')"
      :error="errors.publicKeyPem"
      required
    >
      <template #default="{ id, describedBy, invalid, required, disabled }">
        <textarea
          :id="id"
          v-model="form.publicKeyPem"
          rows="5"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :required="required"
          :disabled="disabled || submitting"
          data-testid="trust-editor-public-key"
        ></textarea>
      </template>
    </FormField>

    <FormField
      v-if="mode === 'rotate'"
      :label="t('trust.editor.graceHours')"
      :help="t('trust.editor.graceHoursHint')"
      :error="errors.graceHours"
      required
    >
      <template #default="{ id, describedBy, invalid, required, disabled }">
        <input
          :id="id"
          v-model.number="form.graceHours"
          type="number"
          min="0"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :required="required"
          :disabled="disabled || submitting"
          data-testid="trust-editor-grace-hours"
        />
      </template>
    </FormField>

    <p class="trust-editor__hint">{{ t('trust.editor.overlapHint') }}</p>

    <div class="trust-editor__actions">
      <button type="button" :disabled="submitting" data-testid="trust-editor-cancel" @click="close">
        {{ t('trust.editor.cancel') }}
      </button>
      <button type="submit" :disabled="submitting" data-testid="trust-editor-submit">
        {{ submitting ? t('trust.editor.saving') : mode === 'create' ? t('trust.editor.submitCreate') : t('trust.editor.submitRotate') }}
      </button>
    </div>
  </form>
</template>

<style scoped>
.trust-editor {
  display: grid;
  gap: var(--space-3);
  max-width: 44rem;
  padding: var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.trust-editor h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.trust-editor input,
.trust-editor textarea {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-subtle);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  font-family: var(--font-family-mono);
  color: var(--color-text);
}

.trust-editor__hint {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.trust-editor__actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}

.trust-editor__actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.trust-editor__actions button[type='submit'] {
  border-color: var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-accent);
  font-weight: var(--font-weight-bold);
}

.trust-editor__actions button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
</style>
