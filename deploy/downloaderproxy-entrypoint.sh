#!/bin/sh
# downloaderproxy entrypoint: start the bundled browser (headless-shell,
# a current Chrome-for-Testing build whose stock fingerprint passes
# publisher WAFs) with a local CDP endpoint, then run the service.
# Chromium stderr stays in the container log for debugging.
set -e

CHROME_ARGS="--no-sandbox --disable-gpu --disable-dev-shm-usage \
 --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 \
 --user-data-dir=/tmp/chromium-profile"

BIN=""
if command -v google-chrome >/dev/null 2>&1; then
  BIN=google-chrome
elif command -v chrome >/dev/null 2>&1; then
  BIN=chrome
elif command -v chromium >/dev/null 2>&1; then
  BIN=chromium
elif command -v headless-shell >/dev/null 2>&1; then
  # headless-shell IS the headless build: no --headless flag, no UA
  # spoofing — its stock fingerprint is what passes the WAFs.
  BIN=headless-shell
fi

if [ -n "$BIN" ]; then
  # One initial start; the Go service supervises it from here on.
  # shellcheck disable=SC2086
  "$BIN" $CHROME_ARGS about:blank >/dev/null 2>&1 &

  # Wait for the CDP endpoint (busybox wget; curl as fallback).
  for _ in $(seq 1 60); do
    if curl -sf -m 3 http://127.0.0.1:9222/json/version >/dev/null 2>&1; then
      echo "CDP endpoint ready ($BIN)" >&2
      break
    fi
    sleep 0.5
  done
else
  echo "warning: no browser binary found; browser lane disabled" >&2
fi

exec /usr/local/bin/downloaderproxy
