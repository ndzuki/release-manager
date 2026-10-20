<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, shallowRef } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import ErrorState from '@/components/common/ErrorState.vue';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();

const username = shallowRef('');
const password = shallowRef('');
const errorMessage = shallowRef('');
const submitting = shallowRef(false);
const sessionExpired = computed(() => route.query.reason === 'expired');

async function handleSubmit(): Promise<void> {
  errorMessage.value = '';
  submitting.value = true;
  try {
    await auth.login(username.value, password.value);
    const destination = auth.returnUrl ?? '/';
    auth.clearReturnUrl();
    await router.replace(destination);
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : t('login.failedFallback');
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <main class="login-page">
    <form class="login-page__form" @submit.prevent="handleSubmit">
      <div>
        <p class="login-page__eyebrow">Release Manager</p>
        <h1 class="login-page__title">{{ t('login.title') }}</h1>
        <p class="login-page__description">{{ t('login.description') }}</p>
      </div>

      <p v-if="sessionExpired" class="login-page__notice" role="status">
        {{ t('login.sessionExpired') }}
      </p>
      <ErrorState v-if="errorMessage" :title="t('login.failedTitle')" :message="errorMessage" />

      <label class="login-page__field">
        <span>{{ t('login.username') }}</span>
        <input v-model="username" autocomplete="username" required :disabled="submitting" />
      </label>

      <label class="login-page__field">
        <span>{{ t('login.password') }}</span>
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          :disabled="submitting"
        />
      </label>

      <button type="submit" :disabled="submitting" class="login-page__submit">
        {{ submitting ? t('login.submitting') : t('login.submit') }}
      </button>
    </form>
  </main>
</template>

<style scoped>
.login-page {
  display: grid;
  min-height: 100vh;
  place-items: center;
  padding: 1.5rem;
  background: var(--color-bg);
}

.login-page__form {
  display: grid;
  width: min(100%, 26rem);
  gap: 1rem;
  padding: 2rem;
  /* Direction-C card: the hairline `--color-border` was ~1.3:1 on the near-black surface,
     so the card edge disappeared in dark; `--color-border-strong` clears the 3:1 non-text
     threshold in both themes. Radius, surface and fill come from the palette tokens
     (TASK-394). */
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
  /* Theme-aware elevation: the shadow is the ink colour at 8%, so it is a soft dark
     cast in light and a soft light halo in dark — the slate literal disappeared on the
     near-black surface (TASK-394). */
  box-shadow: 0 0.75rem 2rem color-mix(in srgb, var(--tx) 8%, transparent);
}

.login-page__eyebrow,
.login-page__description {
  margin: 0;
}

.login-page__description {
  color: var(--color-muted);
}

/* Direction-C eyebrow, matching PageHeader.vue: 12px / 600 / 0.04em / --mu, and no
   uppercase — the auth cards had drifted to 700 + 0.08em + uppercase, which read as a
   different label family from every migrated page (TASK-394). */
.login-page__eyebrow {
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
}

/* The auth cards are full-screen surfaces, so their title takes the 24px step rather than
   PageHeader's 22px `--font-size-h1`; InitPage uses the identical treatment. */
.login-page__title {
  margin: 0.25rem 0;
  color: var(--tx);
  font-size: var(--font-size-xl);
  font-weight: var(--font-weight-heading);
  letter-spacing: -0.01em;
  line-height: var(--line-height-tight);
}

.login-page__notice {
  margin: 0;
  padding: 0.75rem;
  border-radius: 0.375rem;
  background: var(--color-info-soft);
  color: var(--color-primary-hover);
  font-size: var(--font-size-md);
}

.login-page__field {
  display: grid;
  gap: 0.35rem;
  font-size: var(--font-size-md);
}

.login-page__field input {
  padding: 0.65rem 0.75rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
}

.login-page__submit {
  padding: 0.7rem 1rem;
  border: 0;
  border-radius: 0.375rem;
  background: var(--color-primary);
  color: var(--color-on-accent);
  font-weight: 600;
  cursor: pointer;
}

.login-page__submit:disabled {
  cursor: wait;
  opacity: 0.65;
}
</style>
