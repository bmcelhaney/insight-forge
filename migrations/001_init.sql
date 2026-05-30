-- Insight Forge initial schema (adapted from Stitchify Go Framework)
-- All changes must go through additional numbered migrations.

CREATE TABLE IF NOT EXISTS entities (
    id TEXT PRIMARY KEY,                    -- NSN (13 digits) or normalized identifier
    name TEXT,
    metadata JSON,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_refreshed TIMESTAMP
);

CREATE TABLE IF NOT EXISTS data_sources (
    code TEXT PRIMARY KEY,                  -- e.g. WEBFLIS, FPDS, MCRL, OFAC
    name TEXT NOT NULL,
    base_url TEXT,
    priority INTEGER DEFAULT 100,
    enabled BOOLEAN DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS data_snapshots (
    id UUID DEFAULT uuid(),
    entity_id TEXT NOT NULL REFERENCES entities(id),
    source_code TEXT NOT NULL REFERENCES data_sources(code),
    value DECIMAL(14,4),
    currency TEXT DEFAULT 'USD',
    quantity_min INTEGER,
    quantity_max INTEGER,
    reference_id TEXT,
    effective_from DATE,
    effective_to DATE,
    snapshot_at TIMESTAMP NOT NULL,
    raw_response JSON NOT NULL,
    quality_score DECIMAL(5,2),
    is_outlier BOOLEAN DEFAULT FALSE,
    created_by TEXT DEFAULT 'system',
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS processed_results (
    id UUID DEFAULT uuid(),
    entity_id TEXT NOT NULL,
    viability_score DECIMAL(5,2),           -- 0-100
    risk_score DECIMAL(5,2),                -- 0-100
    summary TEXT,
    flags JSON,                             -- geopolitical, regulatory, supplier concentration, etc.
    supplier_data JSON,
    related_nsns JSON,
    based_on_snapshot_ids JSON,
    generated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    generated_by TEXT,
    user_approved BOOLEAN DEFAULT FALSE,
    approved_value DECIMAL(14,4),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id UUID DEFAULT uuid(),
    action TEXT NOT NULL,                   -- INGEST, SYNTHESIZE, EXPORT, APPROVE, etc.
    entity_id TEXT,
    actor TEXT,
    details JSON,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ingestion_jobs (
    id UUID DEFAULT uuid(),
    entity_id TEXT,
    sources TEXT[],
    status TEXT,                            -- pending, running, partial_success, success, failed
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    error TEXT,
    record_count INTEGER DEFAULT 0
);

-- Indexes (from framework guidance)
CREATE INDEX IF NOT EXISTS idx_snapshots_entity_time ON data_snapshots(entity_id, snapshot_at DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_source ON data_snapshots(source_code, snapshot_at DESC);
CREATE INDEX IF NOT EXISTS idx_results_entity ON processed_results(entity_id, generated_at DESC);

-- Seed core data sources (extend as needed)
INSERT OR IGNORE INTO data_sources (code, name, priority) VALUES
    ('WEBFLIS', 'WebFLIS / PUB LOG', 100),
    ('FPDS', 'Federal Procurement Data System', 90),
    ('MCRL', 'Master Cross Reference List', 80),
    ('SANCTIONS', 'Sanctions & Watch Lists', 110);
