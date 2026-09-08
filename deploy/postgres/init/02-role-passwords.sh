#!/bin/sh
# Aligns the role passwords with the environment.
#
# 01-databases.sql creates every role with a literal development password,
# because a plain .sql file cannot read the environment. The compose file, in
# contrast, builds every DSN from ${..._PASSWORD}. As long as both sides keep
# the defaults they agree, but changing a password in .env would leave the
# roles on the old one and every service would fail to authenticate.
#
# This script closes that gap: it runs right after 01-databases.sql during the
# first initialisation of the data directory and re-sets the password of a role
# whenever the corresponding variable is present.
set -eu

set_password() {
    role="$1"
    password="$2"
    [ -n "$password" ] || return 0
    # The value is passed as a bound parameter and quoted by psql, so a password
    # containing quotes cannot break out into SQL.
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
         -v "role=$role" -v "pwd=$password" <<'SQL'
ALTER ROLE :"role" WITH PASSWORD :'pwd';
SQL
    echo "password of role $role taken from the environment"
}

set_password merchant_svc       "${MERCHANT_SVC_PASSWORD:-}"
set_password merchant_migrator  "${MERCHANT_MIGRATOR_PASSWORD:-}"
set_password inventory_svc      "${INVENTORY_SVC_PASSWORD:-}"
set_password inventory_migrator "${INVENTORY_MIGRATOR_PASSWORD:-}"
set_password catalog_svc        "${CATALOG_SVC_PASSWORD:-}"
set_password catalog_migrator   "${CATALOG_MIGRATOR_PASSWORD:-}"
set_password orders_svc         "${ORDERS_SVC_PASSWORD:-}"
set_password orders_migrator    "${ORDERS_MIGRATOR_PASSWORD:-}"
