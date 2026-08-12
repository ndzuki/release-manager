<script setup lang="ts">
import { computed, onMounted, onUnmounted, shallowRef } from 'vue';
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
  <div class="backdrop" role="presentation">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="token-title">
      <LoadingState v-if="generating" message="Generating the one-time token…" />
      <template v-else-if="result && plaintext">
        <header>
          <p class="eyebrow">One-time secret</p>
          <h2 id="token-title">Save the enrollment token now</h2>
          <p>Closing this dialog permanently removes the plaintext from the browser.</p>
        </header>

        <div class="secret">
          <code>{{ plaintext }}</code>
          <button type="button" @click="copy(plaintext)">Copy token</button>
        </div>

        <details>
          <summary>Deployment command</summary>
          <p>Template {{ result.installCommandTemplateVersion }} · {{ result.operatorEndpoint }}</p>
          <pre>{{ installCommand }}</pre>
          <button type="button" @click="copy(installCommand)">Copy command</button>
        </details>

        <p v-if="store.error" class="error" role="alert">{{ store.error.message }}</p>

        <label class="confirmation">
          <input v-model="savedConfirmed" type="checkbox" />
          I saved the token in an approved secret store.
        </label>
        <button type="button" :disabled="!savedConfirmed" @click="close">Close and forget token</button>

        <hr />
        <label class="confirmation">
          <input v-model="discardConfirmed" type="checkbox" />
          I understand discarding revokes this pending token.
        </label>
        <button type="button" class="danger" :disabled="!discardConfirmed || store.saving" @click="discard">
          {{ store.saving ? 'Discarding…' : 'Discard token' }}
        </button>
      </template>

      <template v-else>
        <h2 id="token-title">Token generation did not complete</h2>
        <p class="error" role="alert">{{ store.error?.message ?? 'No token was returned.' }}</p>
        <div class="actions">
          <button type="button" :disabled="generating" @click="generate">Retry generation</button>
          <button type="button" @click="emit('close')">Cancel</button>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: rgb(15 23 42 / 0.72); }
.modal { display: grid; width: min(46rem, 100%); max-height: 90vh; gap: 1rem; overflow: auto; padding: 1.5rem; border-radius: 0.75rem; background: #fff; box-shadow: 0 20px 50px rgb(15 23 42 / 0.35); }
.modal h2, .modal p { margin: 0; }
.eyebrow { color: #b91c1c; font-size: 0.75rem; font-weight: 800; text-transform: uppercase; }
.secret { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid #f59e0b; border-radius: 0.5rem; background: #fffbeb; }
.secret code, pre { overflow-wrap: anywhere; white-space: pre-wrap; }
.actions { display: flex; gap: 0.75rem; justify-content: flex-end; }
button { padding: 0.55rem 0.8rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.5; }
.confirmation { display: flex; gap: 0.5rem; align-items: flex-start; }
.danger { border-color: #ef4444; color: #b91c1c; }
.error { color: #b91c1c; }
</style>
