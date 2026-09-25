#!/bin/sh
# Runs at every container start (nginx's entrypoint runs /docker-entrypoint.d/*.sh
# before nginx itself) and writes the app's runtime settings into config.js, which
# index.html loads before the app. A setting that changes therefore needs only a
# restart of the container, never a rebuild of the image.
set -eu

# A missing address would make the app fall back to localhost — the VIEWER's
# machine, not the server — which looks exactly like a dead API. Refuse instead.
if [ -z "${API_BASE_URL:-}" ]; then
	echo "board-console-web: API_BASE_URL is required (e.g. http://<server>:8040)" >&2
	exit 1
fi

# Escape backslashes and double quotes so a value can never break out of its
# JavaScript string.
js_string() {
	printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

cat > /usr/share/nginx/html/config.js <<CONFIG
window.__BOARD_CONSOLE__ = { apiBaseUrl: "$(js_string "$API_BASE_URL")", apiKey: "$(js_string "${API_KEY:-}")" };
CONFIG
echo "board-console-web: API at $API_BASE_URL"
