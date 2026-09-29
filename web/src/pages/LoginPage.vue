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
  border: 1px solid var(--color-border);
  border-radius: 0.75rem;
  background: var(--color-surface);
  box-shadow: 0 0.75rem 2rem rgb(15 23 42 / 8%);
}

.login-page__eyebrow,
.login-page__description {
  margin: 0;
  color: var(--color-muted);
}

.login-page__eyebrow {
  font-size: var(--font-size-xs);
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.login-page__title {
  margin: 0.25rem 0;
  font-size: var(--font-size-xl);
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
