#!/usr/bin/env bash
# Throwaway services for the e2e suite (unique names and ports, so they never
# touch the developer's own databases):  services.sh up | down
set -euo pipefail
case "${1:-up}" in
  up)
    docker run -d --name laranest-e2e-mysql -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=e2e \
      -e MYSQL_USER=app -e MYSQL_PASSWORD=apppass -p 127.0.0.1:33901:3306 mysql:8.4 >/dev/null 2>&1 || docker start laranest-e2e-mysql >/dev/null
    docker run -d --name laranest-e2e-pg -e POSTGRES_PASSWORD=secret -e POSTGRES_USER=app -e POSTGRES_DB=e2e \
      -p 127.0.0.1:54901:5432 postgres:17 >/dev/null 2>&1 || docker start laranest-e2e-pg >/dev/null
    docker run -d --name laranest-e2e-redis -p 127.0.0.1:63901:6379 redis:7 redis-server --requirepass s3cret >/dev/null 2>&1 || docker start laranest-e2e-redis >/dev/null
    docker run -d --name laranest-e2e-mailpit -p 127.0.0.1:10925:1025 axllent/mailpit >/dev/null 2>&1 || docker start laranest-e2e-mailpit >/dev/null
    for _ in $(seq 60); do docker exec laranest-e2e-mysql mysqladmin -uroot -psecret ping >/dev/null 2>&1 && break; sleep 1; done
    for _ in $(seq 60); do docker exec laranest-e2e-pg pg_isready -U app >/dev/null 2>&1 && break; sleep 1; done
    echo "services up: mysql :33901, pgsql :54901, redis :63901 (s3cret), smtp :10925"
    ;;
  down)
    docker rm -f laranest-e2e-mysql laranest-e2e-pg laranest-e2e-redis laranest-e2e-mailpit >/dev/null 2>&1 || true
    ;;
esac
