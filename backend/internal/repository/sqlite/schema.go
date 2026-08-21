package sqlite

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 30000;
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
	open_price REAL NOT NULL DEFAULT 0,
	high_price REAL NOT NULL DEFAULT 0,
	low_price REAL NOT NULL DEFAULT 0,
	close_price REAL NOT NULL DEFAULT 0,
	previous_close REAL NOT NULL DEFAULT 0,
	change_value REAL NOT NULL DEFAULT 0,
	change_pct REAL NOT NULL,
	volume INTEGER NOT NULL DEFAULT 0,
	turnover INTEGER NOT NULL DEFAULT 0,
    turnover_rate REAL NOT NULL DEFAULT 0,
    amplitude REAL NOT NULL DEFAULT 0,
    quote_available INTEGER NOT NULL DEFAULT 0,
    money_available INTEGER NOT NULL DEFAULT 0,
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
	open_price REAL NOT NULL DEFAULT 0,
	high_price REAL NOT NULL DEFAULT 0,
	low_price REAL NOT NULL DEFAULT 0,
	close_price REAL NOT NULL DEFAULT 0,
	previous_close REAL NOT NULL DEFAULT 0,
	change_value REAL NOT NULL DEFAULT 0,
	change_pct REAL NOT NULL,
	volume INTEGER NOT NULL DEFAULT 0,
	turnover INTEGER NOT NULL DEFAULT 0,
	turnover_rate REAL NOT NULL DEFAULT 0,
	amplitude REAL NOT NULL DEFAULT 0,
	quote_available INTEGER NOT NULL DEFAULT 0,
    money_available INTEGER NOT NULL DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS board_money_5m (
    run_id TEXT NOT NULL,
    snapshot_at TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    rank_type TEXT NOT NULL CHECK (rank_type IN ('industry', 'concept')),
    rank INTEGER NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    dark_money INTEGER NOT NULL,
    regular_money INTEGER NOT NULL,
    main_money_inflow INTEGER NOT NULL,
    money_available INTEGER NOT NULL DEFAULT 0,
    source_time INTEGER NOT NULL,
    fetched_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (trade_date, snapshot_at, rank_type, code)
);

CREATE INDEX IF NOT EXISTS idx_board_money_series
    ON board_money_5m (rank_type, code, snapshot_at);
CREATE INDEX IF NOT EXISTS idx_board_money_rank
    ON board_money_5m (trade_date, rank_type, snapshot_at, rank);

CREATE TABLE IF NOT EXISTS stock_research_5m (
    trade_date TEXT NOT NULL,
    minute_index INTEGER NOT NULL CHECK (minute_index BETWEEN 0 AND 47),
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    money_rank INTEGER NOT NULL,
    dark_money INTEGER NOT NULL,
    regular_money INTEGER NOT NULL,
    main_money_inflow INTEGER NOT NULL,
    money_available INTEGER NOT NULL DEFAULT 0,
    open_price_e4 INTEGER NOT NULL DEFAULT 0,
    high_price_e4 INTEGER NOT NULL DEFAULT 0,
    low_price_e4 INTEGER NOT NULL DEFAULT 0,
    close_price_e4 INTEGER NOT NULL DEFAULT 0,
    volume INTEGER NOT NULL DEFAULT 0,
    turnover INTEGER NOT NULL DEFAULT 0,
    amplitude_ppm INTEGER NOT NULL DEFAULT 0,
    change_pct_ppm INTEGER NOT NULL DEFAULT 0,
    change_value_e4 INTEGER NOT NULL DEFAULT 0,
    turnover_rate_ppm INTEGER NOT NULL DEFAULT 0,
    kline_available INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (trade_date, minute_index, market, code)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_stock_research_series
    ON stock_research_5m (market, code, trade_date, minute_index);
CREATE INDEX IF NOT EXISTS idx_stock_research_rank
    ON stock_research_5m (trade_date, minute_index, money_rank);

CREATE TABLE IF NOT EXISTS stock_archive_quality (
    trade_date TEXT PRIMARY KEY,
    expected_stocks INTEGER NOT NULL,
    expected_points INTEGER NOT NULL DEFAULT 48,
    expected_kline_stocks INTEGER NOT NULL DEFAULT 0,
    money_rows INTEGER NOT NULL DEFAULT 0,
    kline_rows INTEGER NOT NULL DEFAULT 0,
    daily_close_rows INTEGER NOT NULL DEFAULT 0,
    daily_kline_rows INTEGER NOT NULL DEFAULT 0,
    money_archived_at TEXT,
    kline_archived_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS stock_kline_source (
    trade_date TEXT NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    source TEXT NOT NULL,
    point_count INTEGER NOT NULL,
    parser_version TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    run_id TEXT NOT NULL,
    PRIMARY KEY (trade_date, market, code)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_stock_kline_source_type
    ON stock_kline_source (trade_date, source);

CREATE TABLE IF NOT EXISTS daily_archive_manifest (
    trade_date TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('incomplete', 'complete')),
    industry_close_rows INTEGER NOT NULL,
    industry_money_rows INTEGER NOT NULL,
    concept_close_rows INTEGER NOT NULL,
    concept_money_rows INTEGER NOT NULL,
    stock_close_rows INTEGER NOT NULL,
    stock_money_rows INTEGER NOT NULL,
    stock_kline_rows INTEGER NOT NULL,
    stock_daily_kline_rows INTEGER NOT NULL,
    expected_stock_rows INTEGER NOT NULL,
    expected_stock_kline_rows INTEGER NOT NULL,
    code_count INTEGER NOT NULL,
    code_set_sha256 TEXT NOT NULL,
    kline_source_counts_json TEXT NOT NULL,
    darktrade_contract TEXT NOT NULL,
    darktradetick_contract TEXT NOT NULL,
    stock_kline_contract TEXT NOT NULL,
    parser_version TEXT NOT NULL,
    validation_errors_json TEXT NOT NULL,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS daily_archive_revision (
    revision_id TEXT PRIMARY KEY,
    trade_date TEXT NOT NULL,
    revision_no INTEGER NOT NULL,
    previous_revision_id TEXT,
    content_sha256 TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (trade_date, revision_no)
);

CREATE INDEX IF NOT EXISTS idx_daily_archive_revision_date
    ON daily_archive_revision (trade_date, revision_no DESC);

CREATE TABLE IF NOT EXISTS daily_archive_current (
    trade_date TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS daily_feature_set (
    revision_id TEXT PRIMARY KEY,
    trade_date TEXT NOT NULL,
    feature_version TEXT NOT NULL,
    source_revisions_json TEXT NOT NULL,
    generated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS daily_feature (
    revision_id TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    rank_type TEXT NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    primary_industry_code TEXT NOT NULL,
    signed_dark_activity REAL NOT NULL,
    capital_intensity REAL NOT NULL,
    control_coefficient REAL NOT NULL,
    rank_percentile REAL NOT NULL,
    turnover_percentile REAL NOT NULL,
    dark_money_percentile REAL NOT NULL,
    self_turnover_percentile_5 REAL,
    self_turnover_percentile_10 REAL,
    self_turnover_percentile_20 REAL,
    self_turnover_percentile_60 REAL,
    self_dark_money_percentile_5 REAL,
    self_dark_money_percentile_10 REAL,
    self_dark_money_percentile_20 REAL,
    self_dark_money_percentile_60 REAL,
    rank_change_1 INTEGER NOT NULL,
    consecutive_inflow_days INTEGER NOT NULL,
    money_acceleration INTEGER NOT NULL,
    curve_available INTEGER NOT NULL,
    morning_dark_share REAL NOT NULL,
    afternoon_dark_share REAL NOT NULL,
    late_dark_share REAL NOT NULL,
    max_inflow_minute_index INTEGER NOT NULL,
    max_outflow_minute_index INTEGER NOT NULL,
    tail_acceleration INTEGER NOT NULL,
    max_dark_drawdown INTEGER NOT NULL,
    intraday_reversal INTEGER NOT NULL,
    price_money_divergence INTEGER NOT NULL,
    PRIMARY KEY (revision_id, rank_type, market, code)
);

CREATE INDEX IF NOT EXISTS idx_daily_feature_date
    ON daily_feature (trade_date, rank_type, code);

CREATE TABLE IF NOT EXISTS future_return_label (
    signal_revision_id TEXT NOT NULL,
    target_revision_id TEXT NOT NULL,
    signal_date TEXT NOT NULL,
    target_date TEXT NOT NULL,
    horizon INTEGER NOT NULL,
    rank_type TEXT NOT NULL,
    market INTEGER NOT NULL,
    code TEXT NOT NULL,
    return_rate REAL NOT NULL,
    relative_industry_return REAL,
    max_favorable_return REAL NOT NULL,
    max_adverse_return REAL NOT NULL,
    label_version TEXT NOT NULL,
    generated_at TEXT NOT NULL,
    PRIMARY KEY (signal_revision_id, target_revision_id, horizon, rank_type, market, code)
);

CREATE INDEX IF NOT EXISTS idx_future_return_target
    ON future_return_label (target_date, horizon, rank_type, code);

CREATE TABLE IF NOT EXISTS database_maintenance (
    name TEXT PRIMARY KEY,
    completed_at TEXT NOT NULL
);

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

CREATE TABLE IF NOT EXISTS collection_run_rollup (
    rank_type TEXT NOT NULL,
    status TEXT NOT NULL,
    run_count INTEGER NOT NULL,
    record_count INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    latest_success_at TEXT,
    PRIMARY KEY (rank_type, status)
);

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

CREATE TABLE IF NOT EXISTS stock_board_relation_baseline (
    baseline_date TEXT NOT NULL,
    stock_code TEXT NOT NULL,
    stock_market INTEGER NOT NULL,
    stock_name TEXT NOT NULL,
    board_code TEXT NOT NULL,
    board_name TEXT NOT NULL,
    board_type TEXT NOT NULL CHECK (board_type IN ('industry', 'concept')),
    source_order INTEGER NOT NULL,
    relation_source TEXT NOT NULL,
    relation_scope TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    raw_data TEXT NOT NULL,
    PRIMARY KEY (baseline_date, stock_code, board_code, relation_source, relation_scope)
);

CREATE INDEX IF NOT EXISTS idx_relation_baseline_stock
    ON stock_board_relation_baseline (stock_code, baseline_date, board_type, board_code);
CREATE INDEX IF NOT EXISTS idx_relation_baseline_board
    ON stock_board_relation_baseline (board_type, board_code, baseline_date, stock_code);

CREATE TABLE IF NOT EXISTS stock_board_relation_change (
    effective_date TEXT NOT NULL,
    change_type TEXT NOT NULL CHECK (change_type IN ('added', 'removed')),
    stock_code TEXT NOT NULL,
    stock_market INTEGER NOT NULL,
    stock_name TEXT NOT NULL,
    board_code TEXT NOT NULL,
    board_name TEXT NOT NULL,
    board_type TEXT NOT NULL CHECK (board_type IN ('industry', 'concept')),
    source_order INTEGER NOT NULL,
    relation_source TEXT NOT NULL,
    relation_scope TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    run_id TEXT NOT NULL,
    raw_data TEXT NOT NULL,
    PRIMARY KEY (effective_date, change_type, stock_code, board_code, relation_source, relation_scope)
);

CREATE INDEX IF NOT EXISTS idx_relation_change_stock
    ON stock_board_relation_change (stock_code, effective_date, detected_at);
CREATE INDEX IF NOT EXISTS idx_relation_change_board
    ON stock_board_relation_change (board_type, board_code, effective_date, detected_at);

CREATE TABLE IF NOT EXISTS stock_board_relation_current (
    stock_code TEXT NOT NULL,
    stock_market INTEGER NOT NULL,
    stock_name TEXT NOT NULL,
    board_code TEXT NOT NULL,
    board_name TEXT NOT NULL,
    board_type TEXT NOT NULL CHECK (board_type IN ('industry', 'concept')),
    source_order INTEGER NOT NULL,
    relation_source TEXT NOT NULL,
    relation_scope TEXT NOT NULL,
    since_date TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    raw_data TEXT NOT NULL,
    PRIMARY KEY (stock_code, board_code, relation_source, relation_scope)
);

CREATE INDEX IF NOT EXISTS idx_relation_current_stock
    ON stock_board_relation_current (stock_code, board_type, source_order, board_code);
CREATE INDEX IF NOT EXISTS idx_relation_current_board
    ON stock_board_relation_current (board_type, board_code, stock_code);

CREATE TABLE IF NOT EXISTS stock_board_relation_stage (
    run_id TEXT NOT NULL,
    stock_code TEXT NOT NULL,
    stock_market INTEGER NOT NULL,
    stock_name TEXT NOT NULL,
    board_code TEXT NOT NULL,
    board_name TEXT NOT NULL,
    board_type TEXT NOT NULL CHECK (board_type IN ('industry', 'concept')),
    source_order INTEGER NOT NULL,
    relation_source TEXT NOT NULL,
    relation_scope TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    raw_data TEXT NOT NULL,
    PRIMARY KEY (run_id, stock_code, board_code, relation_source, relation_scope)
);

CREATE TABLE IF NOT EXISTS relation_sync_run (
    run_id TEXT PRIMARY KEY,
    trade_date TEXT NOT NULL,
    status TEXT NOT NULL,
    board_count INTEGER NOT NULL,
    relation_count INTEGER NOT NULL,
    added_count INTEGER NOT NULL,
    removed_count INTEGER NOT NULL,
    baseline_built INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    duration_ms INTEGER NOT NULL,
    error_code TEXT NOT NULL,
    error_message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_relation_sync_run_date
    ON relation_sync_run (trade_date, started_at DESC);

`
