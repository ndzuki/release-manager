import { computed, ref, type Ref } from 'vue';
import { t } from '@/i18n/messages';
import type { DefinitionView } from '@/connect/definition-api';

/*
 * TASK-363: the disable confirmation's state moved out of DefinitionsPage.
 *
 * Disabling is destructive and terminal, so the RPC runs only from `confirm` — never
 * from the row button. A lost race reloads the list in the store; this composable
 * adopts the version the server now holds, otherwise the hinted retry would carry the
 * same stale version and fail forever.
 */

export interface DefinitionDisableOptions {
  /** The store's versioned disable; returns false when the server refused it. */
  disable: (definition: DefinitionView) => Promise<boolean>;
  /** The definition the store now holds, after the refusal reloaded the list. */
  freshDefinition: (definitionId: string) => DefinitionView | undefined;
  /** The mapped reason the store holds after a refusal. */
  failureMessage: () => string | null;
}

export interface DefinitionDisable {
  pending: Ref<DefinitionView | null>;
  failure: Ref<string | null>;
  description: Ref<string>;
  arm: (definition: DefinitionView) => void;
  close: () => void;
  confirm: () => Promise<boolean>;
}

export function useDefinitionDisable(options: DefinitionDisableOptions): DefinitionDisable {
  const pending = ref<DefinitionView | null>(null);
  const failure = ref<string | null>(null);
  const description = computed(() =>
    pending.value ? t('definitions.disable.description', { name: pending.value.name }) : '',
  );

  /** Set by the row button; the disable RPC waits for the dialog's confirmation. */
  function arm(definition: DefinitionView): void {
    failure.value = null;
    pending.value = definition;
  }

  function close(): void {
    failure.value = null;
    pending.value = null;
  }

  async function confirm(): Promise<boolean> {
    const target = pending.value;
    if (!target) return false;
    failure.value = null;
    if (!(await options.disable(target))) {
      failure.value = options.failureMessage();
      // A lost race reloaded the list: adopt the version the server now holds,
      // otherwise the hinted retry would carry the same stale version and fail forever.
      const fresh = options.freshDefinition(target.id);
      if (fresh && fresh.version !== target.version) pending.value = fresh;
      return false;
    }
    pending.value = null;
    return true;
  }

  return { pending, failure, description, arm, close, confirm };
}
