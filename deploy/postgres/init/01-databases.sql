-- Creates one logical database and two roles per service:
--   <service>_svc      - runtime role, DML only;
--   <service>_migrator - owner role used by the migration job, DDL allowed.
-- A service role has no privileges on another service's database, so a service
-- physically cannot read or write data it does not own.
--
-- Passwords here are local development defaults; the compose file overrides
-- them from the environment.

CREATE ROLE merchant_svc LOGIN PASSWORD 'merchant_svc';
CREATE ROLE merchant_migrator LOGIN PASSWORD 'merchant_migrator';

CREATE DATABASE merchant OWNER merchant_migrator;
REVOKE ALL ON DATABASE merchant FROM PUBLIC;
GRANT CONNECT ON DATABASE merchant TO merchant_svc;

\connect merchant

GRANT USAGE ON SCHEMA public TO merchant_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE merchant_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO merchant_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE merchant_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO merchant_svc;

\connect postgres

CREATE ROLE inventory_svc LOGIN PASSWORD 'inventory_svc';
CREATE ROLE inventory_migrator LOGIN PASSWORD 'inventory_migrator';

CREATE DATABASE inventory OWNER inventory_migrator;
REVOKE ALL ON DATABASE inventory FROM PUBLIC;
GRANT CONNECT ON DATABASE inventory TO inventory_svc;

\connect inventory

GRANT USAGE ON SCHEMA public TO inventory_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE inventory_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO inventory_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE inventory_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO inventory_svc;

\connect postgres

CREATE ROLE catalog_svc LOGIN PASSWORD 'catalog_svc';
CREATE ROLE catalog_migrator LOGIN PASSWORD 'catalog_migrator';

CREATE DATABASE catalog OWNER catalog_migrator;
REVOKE ALL ON DATABASE catalog FROM PUBLIC;
GRANT CONNECT ON DATABASE catalog TO catalog_svc;

\connect catalog

GRANT USAGE ON SCHEMA public TO catalog_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE catalog_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO catalog_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE catalog_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO catalog_svc;

\connect postgres

CREATE ROLE orders_svc LOGIN PASSWORD 'orders_svc';
CREATE ROLE orders_migrator LOGIN PASSWORD 'orders_migrator';

CREATE DATABASE orders OWNER orders_migrator;
REVOKE ALL ON DATABASE orders FROM PUBLIC;
GRANT CONNECT ON DATABASE orders TO orders_svc;

\connect orders

GRANT USAGE ON SCHEMA public TO orders_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE orders_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO orders_svc;
ALTER DEFAULT PRIVILEGES FOR ROLE orders_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO orders_svc;
