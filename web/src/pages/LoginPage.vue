<script setup lang="ts">
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import ErrorState from '@/components/common/ErrorState.vue';

const router = useRouter();
const auth = useAuthStore();

const username = ref('');
const password = ref('');
const error = ref('');
const loading = ref(false);

async function handleSubmit() {
  error.value = '';
  loading.value = true;
  try {
    await auth.login(username.value, password.value);
    const returnUrl = auth.returnUrl ?? '/';
    auth.clearReturnUrl();
    router.push(returnUrl);
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Login failed';
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <form class="login-page__form" @submit.prevent="handleSubmit">
      <h1 class="login-page__title">Release Manager</h1>

      <ErrorState v-if="error" :message="error" />

      <label class="login-page__field">
        <span>Username</span>
        <input
          v-model="username"
          type="text"
          autocomplete="username"
          required
          :disabled="loading"
        />
      </label>

      <label class="login-page__field">
        <span>Password</span>
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          :disabled="loading"
        />
      </label>

      <button type="submit" :disabled="loading" class="login-page__submit">
        {{ loading ? 'Signing in…' : 'Sign in' }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background: var(--color-bg, #f8fafc);
}

.login-page__form {
  width: 100%;
  max-width: 24rem;
  padding: 2rem;
  background: var(--color-surface, #fff);
  border-radius: 0.5rem;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.login-page__title {
  text-align: center;
  font-size: 1.25rem;
  font-weight: 600;
}

.login-page__field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-size: 0.875rem;
}

.login-page__field input {
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  font-size: 0.9375rem;
}

.login-page__field input:focus {
  outline: none;
  border-color: var(--color-primary, #3b82f6);
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.15);
}

.login-page__submit {
  padding: 0.625rem;
  background: var(--color-primary, #3b82f6);
  color: #fff;
  border: none;
  border-radius: 0.375rem;
  font-size: 0.9375rem;
  font-weight: 500;
  cursor: pointer;
}

.login-page__submit:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
