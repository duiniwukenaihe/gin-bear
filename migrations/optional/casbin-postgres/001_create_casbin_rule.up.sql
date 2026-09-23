-- Apply as a reviewed deployment migration before starting the Casbin service.
-- The column names and widths match the legacy Casbin GORM adapter table.
CREATE TABLE IF NOT EXISTS casbin_rule (
    id BIGSERIAL PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL DEFAULT '',
    v0 VARCHAR(100) NOT NULL DEFAULT '',
    v1 VARCHAR(100) NOT NULL DEFAULT '',
    v2 VARCHAR(100) NOT NULL DEFAULT '',
    v3 VARCHAR(100) NOT NULL DEFAULT '',
    v4 VARCHAR(100) NOT NULL DEFAULT '',
    v5 VARCHAR(100) NOT NULL DEFAULT ''
);

-- Existing duplicate logical rules must be reviewed before applying this index.
-- COALESCE preserves identity for nullable columns in legacy tables.
CREATE UNIQUE INDEX IF NOT EXISTS ux_casbin_rule_identity
    ON casbin_rule (
        ptype,
        COALESCE(v0, ''), COALESCE(v1, ''), COALESCE(v2, ''),
        COALESCE(v3, ''), COALESCE(v4, ''), COALESCE(v5, '')
    );
