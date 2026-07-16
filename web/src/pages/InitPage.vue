<script setup lang="ts">
import { shallowRef } from 'vue';
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import ErrorState from '@/components/common/ErrorState.vue';

const auth = useAuthStore();
const router = useRouter();

const username = shallowRef('admin');
const organizationName = shallowRef('Default Organization');
const password = shallowRef('');
const errorMessage = shallowRef('');
const submitting = shallowRef(false);

async function handleSubmit(): Promise<void> {
  errorMessage.value = '';
  submitting.value = true;
  try {
    await auth.initializeSystem(username.value, password.value, organizationName.value);
    await router.replace({ name: 'Home' });
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to initialize the system.';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <main class="init-page">
    <form class="init-page__form" @submit.prevent="handleSubmit">
      <div>
        <p class="init-page__eyebrow">First-time setup</p>
        <h1 class="init-page__title">Initialize Release Manager</h1>
        <p class="init-page__description">Create the first platform administrator and organization.</p>
      </div>

      <ErrorState v-if="errorMessage" title="Initialization failed" :message="errorMessage" />

      <label class="init-page__field">
        <span>Administrator username</span>
        <input v-model="username" autocomplete="username" required :disabled="submitting" />
      </label>

      <label class="init-page__field">
        <span>Organization name</span>
        <input v-model="organizationName" autocomplete="organization" required :disabled="submitting" />
      </label>

      <label class="init-page__field">
        <span>Password</span>
        <input
          v-model="password"
          type="password"
          autocomplete="new-password"
          minlength="12"
          required
          :disabled="submitting"
        />
      </label>

      <button class="init-page__submit" type="submit" :disabled="submitting">
        {{ submitting ? 'Initializing…' : 'Initialize system' }}
      </button>
    </form>
  </main>
</template>

<style scoped>
.init-page {
  display: grid;
  min-height: 100vh;
  place-items: center;
  padding: 1.5rem;
  background: var(--color-bg, #f8fafc);
}

.init-page__form {
  display: grid;
  width: min(100%, 28rem);
  gap: 1rem;
  padding: 2rem;
  border: 1px solid var(--color-border, #e2e8f0);
  border-radius: 0.75rem;
  background: var(--color-surface, #fff);
  box-shadow: 0 0.75rem 2rem rgb(15 23 42 / 8%);
}

.init-page__eyebrow,
.init-page__description {
  margin: 0;
  color: var(--color-muted, #64748b);
}

.init-page__eyebrow {
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.init-page__title {
  margin: 0.25rem 0;
  font-size: 1.5rem;
}

.init-page__field {
  display: grid;
  gap: 0.35rem;
  font-size: 0.875rem;
}

.init-page__field input {
  padding: 0.65rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
}

.init-page__submit {
  padding: 0.7rem 1rem;
  border: 0;
  border-radius: 0.375rem;
  background: var(--color-primary, #2563eb);
  color: #fff;
  font-weight: 600;
  cursor: pointer;
}

.init-page__submit:disabled {
  cursor: wait;
  opacity: 0.65;
}
</style>
