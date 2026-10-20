<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, onUnmounted, shallowRef } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import Toast from '@/components/common/Toast.vue';
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
// UX-009: the outcome of the last copy attempt, so a refused write is visible instead
// of looking like a successful one. `null` means "nothing copied yet".
const copyNotice = shallowRef<'success' | 'error' | null>(null);
// The dialog owns one title (AppDialog renders the labelled heading), so the
// heading follows the state instead of only existing in the result branch.
const title = computed(() => {
  if (generating.value) return t('operator.token.generating');
  if (result.value && plaintext.value) return t('operator.token.title');
  return t('operator.token.failedTitle');
});
const GATEWAY_PLACEHOLDER = '<CONTROL_PLANE_OPERATOR_GATEWAY>';
const gatewayEndpoint = computed(() => (result.value?.operatorEndpoint ?? '').trim().replace(/\/+$/, ''));

/*
 * Whether the advertised endpoint is an address a CUSTOMER cluster can dial.
 *
 * `operator_endpoint` comes from `cmd/orchestrator/main.go`: it is
 * `https://operator-gateway.dev.release-manager.local:<port>` when the gateway is
 * enabled, and `http://operator:8084` otherwise. Both are control-plane fixture
 * addresses. The `.local` name only resolves on the dev fixture's own network, and
 * `operator` / `*.svc[.cluster.local]` is a Service DNS name that only resolves inside
 * the control-plane cluster. A separate change still has to teach the server to
 * advertise a customer-reachable address (`gateway.advertise_url`), so until it lands
 * the dialog has to recognise these known values instead of copying one into a command
 * that looks ready and cannot connect.
 *
 * A non-TLS endpoint is refused as well: the command carries the enrollment token over
 * that connection, so an `http` gateway would leak the one-time secret. An unparseable
 * value is refused for the same reason: the dialog cannot prove the address reachable,
 * and fail-closed is the safe default for a command that embeds a secret.
 */
type GatewayIssue = 'missing' | 'devLocal' | 'clusterDns' | 'plaintext' | 'invalid';

function classifyGateway(endpoint: string): GatewayIssue | null {
  if (endpoint === '') return 'missing';
  let url: URL;
  try {
    url = new URL(endpoint);
  } catch {
    return 'invalid';
  }
  if (url.protocol !== 'https:') return 'plaintext';
  const host = url.hostname.toLowerCase().replace(/\.$/, '');
  // Cluster DNS is checked before `.local`: `operator.default.svc.cluster.local` ends in
  // both, and the more specific reason is the useful one.
  if (
    host === 'operator' ||
    /^operator\.[^.]+$/.test(host) ||
    host.endsWith('.svc') ||
    host.endsWith('.svc.cluster.local')
  ) {
    return 'clusterDns';
  }
  if (host.endsWith('.local')) return 'devLocal';
  return null;
}

const gatewayIssue = computed(() => classifyGateway(gatewayEndpoint.value));
const gatewayUsable = computed(() => gatewayIssue.value === null);
// An unreachable value stays visible for diagnosis — the warning below explains why the
// command is not armed — while a missing value has nothing to show.
const gatewayDisplay = computed(() =>
  gatewayIssue.value === 'missing' ? t('operator.token.gatewayMissing') : gatewayEndpoint.value,
);
const gatewayWarning = computed(() => {
  switch (gatewayIssue.value) {
    case 'missing':
      return t('operator.token.gatewayMissingWarning');
    case 'devLocal':
      return t('operator.token.gatewayUnreachableWarning', { reason: t('operator.token.gatewayReasonDevLocal') });
    case 'clusterDns':
      return t('operator.token.gatewayUnreachableWarning', { reason: t('operator.token.gatewayReasonClusterDns') });
    case 'plaintext':
      return t('operator.token.gatewayUnreachableWarning', { reason: t('operator.token.gatewayReasonPlaintext') });
    case 'invalid':
      return t('operator.token.gatewayUnreachableWarning', { reason: t('operator.token.gatewayReasonInvalid') });
    default:
      return '';
  }
});

/*
 * The server template keeps the gateway address as a placeholder on purpose: which
 * address a customer cluster can reach is a deployment fact. Substituting a usable
 * endpoint here is what makes the copied command executable instead of one the customer
 * has to finish by hand.
 *
 * The template carries the placeholder twice: as the quoted argument, and inside the
 * instruction comment that tells the reader to replace it. Substituting only the
 * argument would leave a comment asking the user to replace a value the console just
 * wrote, so that instruction block is dropped first. When the gateway is not usable the
 * placeholder has to stay, and the block is replaced by ONE remediation line in the
 * dialog's locale: the command then says exactly which edit is still missing instead of
 * looking ready.
 */
function prepareInstallCommand(template: string, token: string, endpoint: string | null): string {
  const kept: string[] = [];
  let droppingInstruction = false;
  for (const line of template.split('\n')) {
    const isComment = line.trimStart().startsWith('#');
    if (isComment && line.includes(GATEWAY_PLACEHOLDER)) {
      droppingInstruction = true;
      continue;
    }
    if (droppingInstruction) {
      // The instruction spans a contiguous comment block; keep skipping its lines.
      if (isComment) continue;
      droppingInstruction = false;
      if (endpoint === null) kept.push('# ' + t('operator.token.gatewayRemediation'));
    }
    kept.push(line);
  }
  const command = kept.join('\n').replaceAll('${ENROLLMENT_TOKEN}', token);
  // Fail closed: an unusable endpoint keeps the placeholder rather than arming the
  // command with an address the customer cluster cannot resolve.
  return endpoint === null ? command : command.replaceAll(GATEWAY_PLACEHOLDER, endpoint);
}

const installCommand = computed(() => {
  if (!result.value || !plaintext.value) return '';
  const endpoint = gatewayUsable.value ? gatewayEndpoint.value : null;
  return prepareInstallCommand(result.value.installCommandTemplate, plaintext.value, endpoint);
});
// UX-039: a dialog owes the keyboard an exit, but what is irreversible here is the
// PLAINTEXT, not the modal. While a token is on screen — or still being generated,
// where closing would orphan a pending token the user never saw — Escape stays
// disabled and the two acknowledgement checkboxes remain the only way out. The
// failure branch shows no secret, so Escape joins its Cancel button as an exit.
const escapeClosable = computed(() => !generating.value && !plaintext.value);

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

/*
 * UX-009: both failure modes used to be silent. An insecure origin has no
 * `navigator.clipboard` at all (measured 2026-10-17: `undefined` on an http:// page,
 * an object on http://127.0.0.1), and a secure origin can still reject the write when
 * the document is not focused or the permission is denied. Neither reached the user:
 * the click simply did nothing. The plaintext stays selectable either way, so the
 * failure notice points at the manual fallback instead of pretending it copied.
 */
async function copy(value: string): Promise<void> {
  if (!navigator.clipboard) {
    copyNotice.value = 'error';
    return;
  }
  try {
    await navigator.clipboard.writeText(value);
    copyNotice.value = 'success';
  } catch {
    copyNotice.value = 'error';
  }
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
       backdrop click nor Escape may dismiss the dialog while a token is on screen.
       Closing a failed generation is explicit in the same sense — nothing is at risk
       there — and `escapeClosable` is what tells the two branches apart. -->
  <AppDialog
    :open="true"
    :title="title"
    :close-on-backdrop="false"
    :close-on-escape="escapeClosable"
    @close="emit('close')"
  >
    <LoadingState v-if="generating" :message="t('operator.token.generating')" />
    <template v-else-if="result && plaintext">
      <p class="section-label">{{ t('operator.token.oneTimeSecret') }}</p>
      <p class="modal-note">{{ t('operator.token.closeWarning') }}</p>

      <div class="secret">
        <code>{{ plaintext }}</code>
        <button type="button" class="modal-button" @click="copy(plaintext)">{{ t('operator.token.copy') }}</button>
      </div>

      <!-- The deployment command only works against the gateway the control plane
           advertises, so that address is a labelled row of its own rather than a suffix
           of the template-version line — and an unusable endpoint is loud here, before
           the customer copies a command that cannot connect. -->
      <p class="gateway">
        <span class="gateway__label">{{ t('operator.token.gatewayAddress') }}</span>
        <code data-testid="enrollment-gateway">{{ gatewayDisplay }}</code>
      </p>
      <p v-if="gatewayIssue" class="error" role="alert" data-testid="enrollment-gateway-missing">
        {{ gatewayWarning }}
      </p>

      <details>
        <summary>{{ t('operator.token.deploymentCommand') }}</summary>
        <p>{{ t('operator.token.template') }} {{ result.installCommandTemplateVersion }}</p>
        <pre>{{ installCommand }}</pre>
        <button type="button" class="modal-button" @click="copy(installCommand)">{{ t('operator.token.copyCommand') }}</button>
      </details>

      <!-- Copy outcome (UX-009). Success is a polite status; a failure is an assertive
           alert because the user must know the clipboard was not written before they
           close the dialog and lose the plaintext. Both are persistent, matching the
           notification primitive's default and the dialog's own no-timer nature. -->
      <Toast
        v-if="copyNotice === 'success'"
        tone="success"
        testid="enrollment-copy-notice"
        @dismiss="copyNotice = null"
      >
        {{ t('operator.token.copied') }}
      </Toast>
      <Toast
        v-else-if="copyNotice === 'error'"
        tone="error"
        testid="enrollment-copy-notice"
        @dismiss="copyNotice = null"
      >
        {{ t('operator.token.copyFailed') }}
      </Toast>

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
.section-label { margin: 0; color: var(--mu); font-size: var(--font-size-xs); font-weight: 600; letter-spacing: 0.04em; }

.modal-note {
  margin: 0;
}

/* The gateway address is a deployment fact the operator needs for network
   allowlisting, so it gets a labelled row of its own instead of sharing the
   template-version line. */
.gateway {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: baseline;
  margin: 0;
}

.gateway__label {
  color: var(--color-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  text-transform: uppercase;
  letter-spacing: 0.04em;
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
  color: var(--color-error-strong);
}

.modal-button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.error {
  margin: 0;
  color: var(--color-error-strong);
}
</style>
