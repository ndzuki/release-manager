<script setup lang="ts">
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import { t } from '@/i18n/messages';

const auth = useAuthStore();
const router = useRouter();
const message = auth.consumeForbiddenMessage() ?? undefined;
</script>

<template>
  <ForbiddenState :message="message" :action-label="t('action.returnHome')" @action="router.push({ name: 'Home' })" />
</template>
