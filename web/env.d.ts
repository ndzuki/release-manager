/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE?: string;
  readonly VITE_ENABLE_RELEASE_INVENTORY?: string;
  readonly VITE_ENABLE_VALUES_REVISION?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare module '*.vue' {
  import type { DefineComponent } from 'vue';
  const component: DefineComponent<object, object, unknown>;
  export default component;
}

interface ImportMetaEnv {
  readonly VITE_FEATURE_CLUSTER_ROUTING?: string;
  readonly VITE_ARTIFACT_CACHE_ENDPOINT?: string;
  readonly VITE_ARTIFACT_REGISTRY_ENDPOINT?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
