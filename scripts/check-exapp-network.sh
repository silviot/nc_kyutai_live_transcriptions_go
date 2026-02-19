#!/usr/bin/env bash
set -euo pipefail

# Smoke test for Nextcloud ↔ ExApp network wiring.
# This catches regressions where the ExApp hostname is not resolvable
# from inside the Nextcloud container (for example missing network alias).

NEXTCLOUD_CONTAINER="${NEXTCLOUD_CONTAINER:-nextcloud-aio-nextcloud}"
EXAPP_HOST="${EXAPP_HOST:-live_transcription}"
EXAPP_PORT="${EXAPP_PORT:-23000}"

err() {
  echo "ERROR: $*" >&2
  exit 1
}

echo "Checking ExApp DNS and endpoints from container: ${NEXTCLOUD_CONTAINER}"

if ! docker ps --format '{{.Names}}' | grep -qx "${NEXTCLOUD_CONTAINER}"; then
  err "container '${NEXTCLOUD_CONTAINER}' is not running"
fi

RESOLVED_IP="$(docker exec "${NEXTCLOUD_CONTAINER}" sh -lc "getent hosts ${EXAPP_HOST} | awk 'NR==1 {print \$1}'" || true)"
if [[ -z "${RESOLVED_IP}" ]]; then
  err "host '${EXAPP_HOST}' is not resolvable from ${NEXTCLOUD_CONTAINER}"
fi
echo "Resolved ${EXAPP_HOST} -> ${RESOLVED_IP}"

HEALTH_JSON="$(docker exec "${NEXTCLOUD_CONTAINER}" sh -lc "wget -qO- http://${EXAPP_HOST}:${EXAPP_PORT}/healthz" || true)"
if [[ "${HEALTH_JSON}" != *'"status":"healthy"'* ]]; then
  err "health check failed for http://${EXAPP_HOST}:${EXAPP_PORT}/healthz (response: ${HEALTH_JSON})"
fi
echo "Health endpoint OK"

LANG_JSON="$(docker exec "${NEXTCLOUD_CONTAINER}" sh -lc "wget -qO- http://${EXAPP_HOST}:${EXAPP_PORT}/api/v1/languages" || true)"
if [[ "${LANG_JSON}" != *'"en"'* ]]; then
  err "languages endpoint did not return expected data (response: ${LANG_JSON})"
fi
echo "Languages endpoint OK"

echo "PASS: ExApp network wiring looks correct."
