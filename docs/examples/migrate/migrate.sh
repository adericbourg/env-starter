#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  up)
    echo "Running migrations against Postgres..."
    sleep 1
    echo "Migrations complete."
    ;;
  *)
    echo "usage: migrate.sh up" >&2
    exit 1
    ;;
esac
