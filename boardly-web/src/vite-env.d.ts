/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_API_KEY?: string;
}

// Runtime settings from /config.js (see docker/40-runtime-config.sh).
interface Window {
  __BOARDLY__?: { apiBaseUrl?: string; apiKey?: string };
}
