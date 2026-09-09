#!/bin/sh
# Never launch Chrome here: the Go process is the sole browser supervisor.
# exec forwards SIGTERM to the runner, which cancels downloads and reaps Chrome.
set -eu
exec /usr/local/bin/downloaderworker "$@"
