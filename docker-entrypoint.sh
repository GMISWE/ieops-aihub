#!/bin/sh
set -e

if [ "$1" = "migrate-up" ]; then
    echo "Running database migrations..."
    exec goose -dir /migrations postgres "$DATABASE_URL" up
fi

exec /usr/local/bin/aihub "$@"
