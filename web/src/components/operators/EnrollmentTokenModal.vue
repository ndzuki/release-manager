<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, onUnmounted, shallowRef } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { useOperatorStore } from '@/stores/operator';
import type { EnrollmentTokenMetadata } from '@/types/operator';

interface Props {
  customerId: string;
  clusterId: string;
  replacePendingToken?: boolean;
}

const props = withDefaults(defineProps<Props>(), { replacePendingToken: false });
const emit = defineEmits<{ close: [] }>();
const store = useOperatorStore();
const result = shallowRef<EnrollmentTokenMetadata | null>(null);
const plaintext = shallowRef<string | null>(null);
const savedConfirmed = shallowRef(false);
const discardConfirmed = shallowRef(false);
const generating = shallowRef(false);
// The dialog owns one title (AppDialog renders the labelled heading), so the
// heading follows the state instead of only existing in the result branch.
const title = computed(() => {
  if (generating.value) return t('operator.token.generating');
  if (result.value && plaintext.value) return t('operator.token.title');
  return t('operator.token.failedTitle');
});
const installCommand = computed(() => {
  if (!result.value || !plaintext.value) return '';
  return result.value.installCommandTemplate.replace('${ENROLLMENT_TOKEN}', plaintext.value);
});

async function generate(): Promise<void> {
  generating.value = true;
  try {
    const generated = await store.generateToken(props.customerId, props.clusterId, props.replacePendingToken);
    if (generated) {
      const { token, ...metadata } = generated;
      result.value = metadata;
      plaintext.value = token;
    } else {
      result.value = null;
      plaintext.value = null;
    }
  } finally {
    generating.value = false;
  }
}

async function copy(value: string): Promise<void> {
  await navigator.clipboard.writeText(value);
}

function clearPlaintext(): void {
  plaintext.value = null;
  result.value = null;
}

function close(): void {
  if (!savedConfirmed.value || !plaintext.value) return;
  clearPlaintext();
  emit('close');
}

async function discard(): Promise<void> {
  if (!discardConfirmed.value || !plaintext.value) return;
  if (await store.discardPending(props.customerId, props.clusterId)) {
    clearPlaintext();
    emit('close');
  }
}

onMounted(generate);
onUnmounted(clearPlaintext);
</script>

<template>
  <!-- One-time secret: the plaintext is removed on close, so neither a stray
       backdrop click nor Escape may dismiss this dialog. Closing is an explicit
       acknowledgement (see the two checkboxes). -->
  <AppDialog
    :open="true"
    :title="title"
    :close-on-backdrop="false"
    :close-on-escape="false"
    @close="emit('close')"
  >
    <LoadingState v-if="generating" :message="t('operator.token.generating')" />
    <template v-else-if="result && plaintext">
      <p class="eyebrow">{{ t('operator.token.oneTimeSecret') }}</p>
      <p class="modal-note">{{ t('operator.token.closeWarning') }}</p>

      <div class="secret">
        <code>{{ plaintext }}</code>
        <button type="button" class="modal-button" @click="copy(plaintext)">{{ t('operator.token.copy') }}</button>
      </div>

      <details>
        <summary>{{ t('operator.token.deploymentCommand') }}</summary>
        <p>{{ t('operator.token.template') }} {{ result.installCommandTemplateVersion }} · {{ result.operatorEndpoint }}</p>
        <pre>{{ installCommand }}</pre>
        <button type="button" class="modal-button" @click="copy(installCommand)">{{ t('operator.token.copyCommand') }}</button>
      </details>

      <p v-if="store.error" class="error" role="alert">{{ store.error.message }}</p>

      <label class="confirmation">
        <input v-model="savedConfirmed" type="checkbox" />
        {{ t('operator.token.saved') }}
      </label>
      <button type="button" class="modal-button" :disabled="!savedConfirmed" @click="close">
        {{ t('operator.token.closeAndForget') }}
      </button>

      <hr />
      <label class="confirmation">
        <input v-model="discardConfirmed" type="checkbox" />
        {{ t('operator.token.discardAck') }}
      </label>
      <button
        type="button"
        class="modal-button modal-button--danger"
        :disabled="!discardConfirmed || store.saving"
        @click="discard"
      >
        {{ store.saving ? t('operator.token.discarding') : t('operator.token.discard') }}
      </button>
    </template>

    <template v-else>
      <p class="error" role="alert">{{ store.error?.message ?? t('operator.token.noToken') }}</p>
      <div class="actions">
        <button type="button" class="modal-button" :disabled="generating" @click="generate">{{ t('operator.token.retryGeneration') }}</button>
        <button type="button" class="modal-button" @click="emit('close')">{{ t('action.cancel') }}</button>
      </div>
    </template>
  </AppDialog>
</template>

<style scoped>
.eyebrow {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-xs);
  font-weight: 800;
  text-transform: uppercase;
}

.modal-note {
  margin: 0;
}

.secret {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-warning-solid);
  border-radius: var(--radius-lg);
  background: var(--color-warning-surface);
}

.secret code,
pre {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}

.confirmation {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
}

/* Body buttons are not slotted into AppDialog's footer, so they carry their own
   baseline rather than inheriting the footer's. */
.modal-button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  font: inherit;
  cursor: pointer;
}

.modal-button--danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.modal-button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.error {
  margin: 0;
  color: var(--color-error);
}
</style>
