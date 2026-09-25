// Runtime settings. Empty in development (the app then reads VITE_* from
// .env.local); the Docker image rewrites this file at every container start
// from API_BASE_URL / API_KEY — see docker/40-runtime-config.sh.
window.__BOARDLY__ = {};
