#!/bin/sh
# First-run bootstrap for the container: initialize the data dir/master key if absent, then start
# the gateway. On a fresh volume the server boots into browser setup mode — open the mapped port
# (default http://127.0.0.1:8787/) and the wizard walks through vault, provider, and token.
# Everything is also scriptable:  docker compose exec cli-os l00prite ...
set -e
: "${LOOPRITE_HOME:=/data}"
export LOOPRITE_HOME

if [ ! -f "$LOOPRITE_HOME/master.key" ]; then
  echo "[entrypoint] initializing $LOOPRITE_HOME"
  l00prite init
  echo "[entrypoint] no provider configured yet — open the mapped port in a browser to finish setup."
fi

exec l00prite serve
