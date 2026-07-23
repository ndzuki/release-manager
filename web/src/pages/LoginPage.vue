<script setup lang="ts">
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
    errorMessage.value = error instanceof Error ? error.message : 'Unable to sign in.';
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
        <h1 class="login-page__title">Sign in</h1>
        <p class="login-page__description">Use your server-managed session to continue.</p>
      </div>

      <p v-if="sessionExpired" class="login-page__notice" role="status">
        Your session expired. Sign in again to return to your previous page.
      </p>
      <ErrorState v-if="errorMessage" title="Sign in failed" :message="errorMessage" />

      <label class="login-page__field">
        <span>Username</span>
        <input v-model="username" autocomplete="username" required :disabled="submitting" />
      </label>

      <label class="login-page__field">
        <span>Password</span>
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          :disabled="submitting"
        />
      </label>

      <button type="submit" :disabled="submitting" class="login-page__submit">
        {{ submitting ? 'Signing in…' : 'Sign in' }}
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
  background: var(--color-bg, #f8fafc);
}

.login-page__form {
  display: grid;
  width: min(100%, 26rem);
  gap: 1rem;
  padding: 2rem;
  border: 1px solid var(--color-border, #e2e8f0);
  border-radius: 0.75rem;
  background: var(--color-surface, #fff);
  box-shadow: 0 0.75rem 2rem rgb(15 23 42 / 8%);
}

.login-page__eyebrow,
.login-page__description {
  margin: 0;
  color: var(--color-muted, #64748b);
}

.login-page__eyebrow {
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.login-page__title {
  margin: 0.25rem 0;
  font-size: 1.5rem;
}

.login-page__notice {
  margin: 0;
  padding: 0.75rem;
  border-radius: 0.375rem;
  background: #eff6ff;
  color: #1d4ed8;
  font-size: 0.875rem;
}

.login-page__field {
  display: grid;
  gap: 0.35rem;
  font-size: 0.875rem;
}

.login-page__field input {
  padding: 0.65rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
}

.login-page__submit {
  padding: 0.7rem 1rem;
  border: 0;
  border-radius: 0.375rem;
  background: var(--color-primary, #2563eb);
  color: #fff;
  font-weight: 600;
  cursor: pointer;
}

.login-page__submit:disabled {
  cursor: wait;
  opacity: 0.65;
}
</style>
