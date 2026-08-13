package sqlite

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS rank_intraday_work (
    run_id TEXT NOT NULL,
    snapshot_at TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    rank_type TEXT NOT NULL CHECK (rank_type IN ('industry', 'concept')),
    rank INTEGER NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    quote_time TEXT NOT NULL,
    latest_price_raw INTEGER NOT NULL,
    change_pct REAL NOT NULL,
    dark_money INTEGER NOT NULL,
    regular_money INTEGER NOT NULL,
    main_money_inflow INTEGER NOT NULL,
    dark_activity REAL NOT NULL,
    dark_inflow_ratio REAL NOT NULL,
    up_count INTEGER NOT NULL,
    flat_count INTEGER NOT NULL,
    down_count INTEGER NOT NULL,
    leader_name TEXT NOT NULL,
    leader_code TEXT NOT NULL,
    source_version INTEGER NOT NULL,
    source_sort_flag INTEGER NOT NULL,
    source_descending INTEGER NOT NULL,
    fetched_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (trade_date, snapshot_at, rank_type, code)
);

CREATE INDEX IF NOT EXISTS idx_intraday_latest
    ON rank_intraday_work (trade_date, rank_type, snapshot_at DESC, rank);
CREATE INDEX IF NOT EXISTS idx_intraday_series
    ON rank_intraday_work (trade_date, rank_type, code, snapshot_at);

CREATE TABLE IF NOT EXISTS rank_snapshot (
    run_id TEXT NOT NULL,
    snapshot_at TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    requested_date TEXT NOT NULL,
    snapshot_kind TEXT NOT NULL CHECK (snapshot_kind IN ('research_5m', 'daily_close')),
    rank_type TEXT NOT NULL CHECK (rank_type IN ('stock', 'industry', 'concept')),
    rank INTEGER NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    quote_time TEXT NOT NULL,
    latest_price_raw INTEGER NOT NULL,
    change_pct REAL NOT NULL,
    dark_money INTEGER NOT NULL,
    regular_money INTEGER NOT NULL,
    main_money_inflow INTEGER NOT NULL,
    dark_activity REAL NOT NULL,
    dark_inflow_ratio REAL NOT NULL,
    up_count INTEGER NOT NULL,
    flat_count INTEGER NOT NULL,
    down_count INTEGER NOT NULL,
    leader_name TEXT NOT NULL,
    leader_code TEXT NOT NULL,
    source_version INTEGER NOT NULL,
    source_sort_flag INTEGER NOT NULL,
    source_descending INTEGER NOT NULL,
    fetched_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (trade_date, snapshot_kind, snapshot_at, rank_type, code)
);

CREATE INDEX IF NOT EXISTS idx_snapshot_rank
    ON rank_snapshot (trade_date, snapshot_kind, rank_type, snapshot_at, rank);
CREATE INDEX IF NOT EXISTS idx_snapshot_series
    ON rank_snapshot (snapshot_kind, rank_type, code, snapshot_at);

CREATE TABLE IF NOT EXISTS raw_response (
    run_id TEXT NOT NULL,
    snapshot_at TEXT NOT NULL,
    snapshot_kind TEXT NOT NULL,
    rank_type TEXT NOT NULL,
    page INTEGER NOT NULL,
    content_encoding TEXT NOT NULL,
    compression TEXT NOT NULL,
    body BLOB NOT NULL,
    fetched_at TEXT NOT NULL,
    PRIMARY KEY (snapshot_kind, snapshot_at, rank_type, page)
);

CREATE TABLE IF NOT EXISTS collection_run (
    run_id TEXT PRIMARY KEY,
    snapshot_at TEXT NOT NULL,
    snapshot_kind TEXT NOT NULL,
    rank_type TEXT NOT NULL,
    status TEXT NOT NULL,
    requested_date TEXT NOT NULL,
    actual_trade_date TEXT NOT NULL,
    expected_total INTEGER NOT NULL,
    fetched_total INTEGER NOT NULL,
    page_count INTEGER NOT NULL,
    attempt_count INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    duration_ms INTEGER NOT NULL,
    error_code TEXT NOT NULL,
    error_message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_collection_run_date
    ON collection_run (requested_date, snapshot_at DESC);

CREATE TABLE IF NOT EXISTS research_quality (
    trade_date TEXT NOT NULL,
    rank_type TEXT NOT NULL,
    expected_minutes INTEGER NOT NULL,
    collected_minutes INTEGER NOT NULL,
    expected_research INTEGER NOT NULL,
    collected_research INTEGER NOT NULL,
    expected_daily_close INTEGER NOT NULL DEFAULT 1,
    collected_daily_close INTEGER NOT NULL DEFAULT 0,
    missing_minutes_json TEXT NOT NULL,
    missing_research_json TEXT NOT NULL,
    missing_daily_close_json TEXT NOT NULL DEFAULT '["15:00"]',
    compacted_at TEXT NOT NULL,
    PRIMARY KEY (trade_date, rank_type)
);
`
