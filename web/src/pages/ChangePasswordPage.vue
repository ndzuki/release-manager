<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, ref } from 'vue';
import { useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import { changePassword, mapChangePasswordError, passwordTooLong, PASSWORD_MAX_BYTES } from '@/connect/auth-api';
import { useAuthStore } from '@/stores/auth';

/*
 * Password change (A4 of the UX plan's missing surfaces, REQ-025).
 *
 * Validation is deliberately UX-only: length/confirm/same-as-old. REQ-025 records
 * the product decision "密码策略=无强度校验" (no strength policy), so this page must
 * not invent a complexity rule the product explicitly declined; the server keeps
 * no policy either ("length and character rules are the caller's responsibility").
 *
 * On success the server revokes every session of the user (AC-025-03), so the page
 * clears the local session and returns to the login screen instead of letting the
 * next request fail with 401.
 */
const router = useRouter();
const auth = useAuthStore();

const oldPassword = ref('');
const newPassword = ref('');
const confirmPassword = ref('');
const submitting = ref(false);
const failure = ref<string | null>(null);
const changed = ref(false);
// Validation is only surfaced after the user tries to submit: an empty form that
// greets you with "请填写全部三项" reads as an error the user did not make.
const touched = ref(false);

const lengthError = computed(() => {
  if (!oldPassword.value || !newPassword.value || !confirmPassword.value) return '请填写全部三项';
  if (newPassword.value !== confirmPassword.value) return '两次输入的新密码不一致';
  if (newPassword.value === oldPassword.value) return '新密码不能与当前密码相同';
  if (passwordTooLong(newPassword.value)) return `新密码过长（上限 ${PASSWORD_MAX_BYTES} 字节）`;
  return '';
});

const validationError = computed(() => (touched.value ? lengthError.value : ''));

async function submit(): Promise<void> {
  touched.value = true;
  if (lengthError.value || submitting.value) return;
  submitting.value = true;
  failure.value = null;
  try {
    await changePassword(oldPassword.value, newPassword.value);
    changed.value = true;
    oldPassword.value = '';
    newPassword.value = '';
    confirmPassword.value = '';
    // The session is gone server-side; drop it locally and go back to Login.
    auth.clearSession('anonymous');
    await new Promise((resolve) => setTimeout(resolve, 1200));
    await router.replace({ name: 'Login' });
  } catch (error) {
    failure.value = mapChangePasswordError(error).message;
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <section class="change-password">
    <header>
      <p class="eyebrow">{{ t('account.security') }}</p>
      <h1>修改密码</h1>
      <p class="change-password__hint">
        修改成功后所有会话都会被撤销，需要重新登录。
      </p>
    </header>

    <p v-if="changed" class="change-password__success" role="status" data-testid="change-password-success">
      密码已修改，正在跳转到登录页…
    </p>

    <ErrorState
      v-if="failure"
      title="修改密码失败"
      :message="failure"
      action-label="重试"
      @action="submit"
    />

    <form class="change-password__form" @submit.prevent="submit">
      <label>
        当前密码
        <input v-model="oldPassword" type="password" name="oldPassword" autocomplete="current-password" />
      </label>
      <label>
        新密码
        <input v-model="newPassword" type="password" name="newPassword" autocomplete="new-password" />
      </label>
      <label>
        确认新密码
        <input v-model="confirmPassword" type="password" name="confirmPassword" autocomplete="new-password" />
      </label>

      <p class="change-password__limit">
        密码只做非空与一致性校验；上限 {{ PASSWORD_MAX_BYTES }} 字节（bcrypt 限制），不设复杂度要求。
      </p>

      <p v-if="validationError" class="change-password__error" role="alert" data-testid="change-password-validation">
        {{ validationError }}
      </p>

      <div class="change-password__actions">
        <RouterLink :to="{ name: 'Home' }">返回</RouterLink>
        <button type="submit" :disabled="submitting" data-testid="change-password-submit">
          {{ submitting ? '提交中…' : '修改密码' }}
        </button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.change-password {
  display: grid;
  gap: var(--space-4);
  max-width: 32rem;
  padding: var(--space-5);
}

.change-password h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.change-password__hint {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.change-password__success {
  margin: 0;
  padding: var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.change-password__form {
  display: grid;
  gap: var(--space-3);
}

.change-password__form label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.change-password__form input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.change-password__limit {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-xs);
}

.change-password__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.change-password__actions {
  display: flex;
  gap: var(--space-3);
  align-items: center;
  justify-content: flex-end;
}

.change-password__actions button {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-md);
  background: var(--color-primary);
  color: var(--color-on-accent);
  font-weight: var(--font-weight-medium);
  cursor: pointer;
}

.change-password__actions button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
</style>
