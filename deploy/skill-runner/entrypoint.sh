#!/bin/sh
# Writes the run's JSON arguments to /work/args.json, then runs the script given as arguments.
# The arguments are copied with printf '%s', never parsed or evaluated by the shell.
set -eu
printf '%s' "$NESTOR_ARGS" > /work/args.json
unset NESTOR_ARGS
exec "$@"
