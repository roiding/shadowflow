"""Small offline LightGBM scaffold with explicit labels and purged date splits.

Requires prepared CSVs described in README.md. Does not fetch, repair, invent
labels, simulate fills or claim that prediction metrics are trading returns.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile

import lightgbm as lgb
import numpy as np
import pandas as pd


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def check_dates(frame, columns):
    for column in columns:
        parsed = pd.to_datetime(frame[column], format="%Y-%m-%d", errors="raise")
        require(parsed.notna().all(), f"{column} has missing dates")
        require((parsed.dt.strftime("%Y-%m-%d") == frame[column]).all(), f"{column} is not ISO date")


def midnight_utc(date_string):
    return pd.Timestamp(date_string, tz="Asia/Shanghai").tz_convert("UTC")


def timestamps(values, label):
    require(values.astype(str).str.contains(r"(?:Z|[+-]\d{2}:\d{2})$", regex=True).all(), f"{label} requires explicit timezones")
    result = pd.to_datetime(values, utc=True, errors="raise")
    require(result.notna().all(), f"missing {label}")
    return result


def metrics(frame, prediction, train_mean):
    scored = frame[["signal_date", "y"]].copy()
    scored["prediction"] = prediction
    daily_ic = []
    for _, day in scored.groupby("signal_date", sort=True):
        if len(day) < 20 or day.y.nunique() < 2 or day.prediction.nunique() < 2:
            continue
        value = day.y.rank().corr(day.prediction.rank())
        if np.isfinite(value):
            daily_ic.append(float(value))
    return {
        "rows": len(scored), "signal_dates": int(scored.signal_date.nunique()),
        "mse": float(np.mean((scored.y.to_numpy() - prediction) ** 2)),
        "constant_train_mean_mse": float(np.mean((scored.y - train_mean) ** 2)),
        "mae": float(np.mean(np.abs(scored.y.to_numpy() - prediction))),
        "daily_rank_ic_mean": float(np.mean(daily_ic)) if daily_ic else None,
        "daily_rank_ic_dates": len(daily_ic),
        "note": "Prediction diagnostics only. Overlapping h-day labels are not independent; no strategy P&L is reported.",
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--features", type=Path, required=True)
    parser.add_argument("--labels", type=Path, required=True)
    parser.add_argument("--calendar", type=Path, required=True)
    parser.add_argument("--as-of", required=True, help="Frozen dataset timestamp with explicit timezone")
    parser.add_argument("--config", type=Path, default=Path(__file__).with_name("config.json"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print("Validating frozen dataset contracts", file=sys.stderr, flush=True)
    require(not args.output.exists(), "output must be a new directory")
    cfg = json.loads(args.config.read_text())
    availability_mode = cfg.get("availability_mode", "strict_vintage")
    require(availability_mode in {"strict_vintage", "reconstructed_research"}, "unknown availability mode")
    dataset_as_of = pd.Timestamp(args.as_of)
    require(dataset_as_of.tzinfo is not None, "--as-of requires an explicit timezone")
    dataset_as_of = dataset_as_of.tz_convert("UTC")
    calendar_payload = json.loads(args.calendar.read_text())
    calendar = calendar_payload["trading_dates"]
    require(calendar == sorted(set(calendar)) and len(calendar) > 0, "calendar must contain unique sorted trading dates")
    check_dates(pd.DataFrame({"date": calendar}), ["date"])
    date_indexes = {date: index for index, date in enumerate(calendar)}
    names = cfg["feature_columns"]
    require(len(names) == len(set(names)) and len(names) > 0, "feature whitelist must be nonempty/unique")
    require(cfg["train_start"] < cfg["validation_start"] < cfg["test_start"] <= cfg["test_end"], "invalid date boundaries")
    forbidden = {"y", "return_rate", "target_date", "exit_date", "label_ready_at", "adj_factor"}
    require(not set(names) & forbidden, "future labels or absolute factors cannot be features")
    features = pd.read_csv(args.features, dtype={"security": str, "signal_date": str, "dataset_id": str})
    labels = pd.read_csv(args.labels, dtype={"security": str, "signal_date": str, "dataset_id": str})
    key = ["dataset_id", "security", "signal_date"]
    require(set(features.columns) == set(key + ["signal_at", "feature_available_at", "eligible_asof"] + names), "features must contain only the documented metadata and whitelisted features")
    require(set(key + ["entry_date", "exit_date", "horizon", "label_version", "action_version", "status", "y", "label_ready_at"]) <= set(labels.columns), "labels are missing required contract columns")
    for frame in (features, labels):
        require(not frame.duplicated(key).any(), "duplicate security/signal date/dataset rows")
        require(frame[key].notna().all().all(), "missing row identity")
        require(frame.dataset_id.nunique() == 1, "exactly one frozen dataset per run")
    require(set(features.dataset_id) == set(labels.dataset_id), "features and labels have different dataset IDs")
    require((labels.label_version == cfg["label_version"]).all(), "label version mismatch")
    require((labels.horizon == cfg["horizon"]).all(), "horizon mismatch")
    require(labels.action_version.notna().all() and labels.action_version.astype(str).str.strip().ne("").all(), "missing corporate-action version")
    check_dates(features, ["signal_date"])
    features["signal_at"] = timestamps(features.signal_at, "signal_at")
    features["feature_available_at"] = timestamps(features.feature_available_at, "feature_available_at")
    require(features.signal_at.notna().all() and features.feature_available_at.notna().all(), "missing availability timestamps")
    require((features.feature_available_at <= features.signal_at).all(), "future feature observed after signal")
    require((features.signal_at.dt.tz_convert("Asia/Shanghai").dt.strftime("%Y-%m-%d") == features.signal_date).all(), "signal timestamp/date mismatch")
    # Eligibility is determined at signal time. Never use future fill flags
    # to select a universe or silently delete limit-down exit observations.
    require(features.eligible_asof.isin([0, 1]).all(), "eligible_asof must be a verified 0/1 flag")
    frame = features.merge(labels, on=key, how="left", validate="one_to_one", suffixes=("", "_label"))
    universe = frame[(frame.eligible_asof == 1) & (frame.signal_date >= cfg["train_start"])
                     & (frame.signal_date <= cfg["test_end"])].copy()
    require(len(universe) > 0, "no eligible signal rows")
    status_counts = universe.status.fillna("label_row_missing").value_counts().to_dict()
    coverage = float((universe.status == "ok").mean())
    require(coverage >= cfg["minimum_label_coverage"], f"label coverage {coverage:.2%} below gate: {status_counts}")
    frame = universe[universe.status == "ok"].copy()
    check_dates(frame, ["entry_date", "exit_date"])
    for row in frame[["signal_date", "entry_date", "exit_date"]].itertuples(index=False):
        index = date_indexes.get(row.signal_date)
        require(index is not None and index + 1 + cfg["horizon"] < len(calendar), "calendar does not cover label interval")
        require(row.entry_date == calendar[index + 1] and row.exit_date == calendar[index + 1 + cfg["horizon"]], "label interval differs from exchange-calendar horizon; do not skip missing dates")
    require((frame.signal_date < frame.entry_date).all() and (frame.entry_date < frame.exit_date).all(), "invalid label interval/T+1")
    frame["y"] = pd.to_numeric(frame.y, errors="raise")
    require(np.isfinite(frame.y).all(), "label NaN/Inf")
    require((frame.y >= -1).all(), "long-only gross-wealth labels below -100%")
    frame["label_ready_at"] = timestamps(frame.label_ready_at, "label_ready_at")
    require(frame.label_ready_at.notna().all(), "missing label availability")
    require((frame.label_ready_at.dt.tz_convert("Asia/Shanghai").dt.strftime("%Y-%m-%d") >= frame.exit_date).all(), "label marked ready before its target date")
    require((frame.label_ready_at <= dataset_as_of).all(), "labels were not known at the frozen dataset timestamp")
    for name in names:
        frame[name] = pd.to_numeric(frame[name], errors="raise")
        require(not np.isinf(frame[name]).any(), f"infinite feature: {name}")
        require(frame[name].notna().mean() >= 0.95, f"feature coverage below 95%: {name}")
    frame = frame.sort_values(["signal_date", "security"]).reset_index(drop=True)
    validation_cutoff = midnight_utc(cfg["validation_start"])
    test_cutoff = midnight_utc(cfg["test_start"])
    train_mask = ((frame.signal_date < cfg["validation_start"])
                  & (frame.exit_date < cfg["validation_start"]))
    validation_mask = ((frame.signal_date >= cfg["validation_start"])
                       & (frame.signal_date < cfg["test_start"])
                       & (frame.exit_date < cfg["test_start"]))
    if availability_mode == "strict_vintage":
        train_mask &= frame.label_ready_at < validation_cutoff
        validation_mask &= frame.label_ready_at < test_cutoff
    # Reconstructed mode still purges overlapping future price intervals.
    # It explicitly cannot prove that later-verified historical label
    # revisions were available to a historical trading system.
    train, validation = frame[train_mask], frame[validation_mask]
    test = frame[(frame.signal_date >= cfg["test_start"]) & (frame.signal_date <= cfg["test_end"])]
    for name, subset in [("train", train), ("validation", validation), ("test", test)]:
        require(subset.signal_date.nunique() >= cfg["minimum_dates"][name], f"too few {name} dates; do not count thousands of same-day stocks as independent days")
    # Equal total weight per date keeps a wider stock universe from giving
    # some dates more influence. No test date enters early stopping.
    weights = 1.0 / train.groupby("signal_date").security.transform("size")
    weights = weights / weights.mean()
    training = lgb.Dataset(train[names], label=train.y, weight=weights)
    validating = lgb.Dataset(validation[names], label=validation.y, reference=training)
    print(f"Training: {len(train)} rows; validation: {len(validation)} rows; test: {len(test)} rows", file=sys.stderr, flush=True)
    model = lgb.train(cfg["parameters"], training, num_boost_round=cfg["num_boost_round"],
                      valid_sets=[validating], valid_names=["validation"],
                      callbacks=[lgb.early_stopping(cfg["early_stopping_rounds"], verbose=False)])
    print("Scoring validation and held-out test dates", file=sys.stderr, flush=True)
    train_mean = float(train.y.mean())
    validation_prediction = model.predict(validation[names], num_iteration=model.best_iteration)
    test_prediction = model.predict(test[names], num_iteration=model.best_iteration)
    report = {
        "dataset_id": str(frame.dataset_id.iloc[0]), "label_version": cfg["label_version"],
        "action_versions": sorted(frame.action_version.astype(str).unique().tolist()),
        "availability_mode": availability_mode,
        "vintage_note": "As-of checks rely on supplied source timestamps" if availability_mode == "strict_vintage" else "Retrospective reconstruction; not evidence of historical point-in-time availability",
        "horizon": cfg["horizon"], "features_sha256": sha256(args.features), "labels_sha256": sha256(args.labels),
        "label_core_sha256": sha256(Path(__file__).with_name("label_core.py")),
        "calendar_sha256": sha256(args.calendar), "dataset_as_of": dataset_as_of.isoformat(),
        "training_code_sha256": sha256(Path(__file__)), "config_sha256": sha256(args.config),
        "label_coverage": coverage, "label_status_counts": status_counts,
        "train_dates": int(train.signal_date.nunique()), "train_rows": len(train),
        "train_mean_label": train_mean, "best_iteration": model.best_iteration,
        "validation": metrics(validation, validation_prediction, train_mean),
        "test": metrics(test, test_prediction, train_mean),
        "versions": {"numpy": np.__version__, "pandas": pd.__version__, "lightgbm": lgb.__version__},
        "feature_missing_rates": {name: float(frame[name].isna().mean()) for name in names},
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    print("Publishing the model and frozen run metadata", file=sys.stderr, flush=True)
    with tempfile.TemporaryDirectory(prefix=".quant-run-", dir=args.output.parent) as temp:
        staging = Path(temp) / "result"
        staging.mkdir()
        model.save_model(str(staging / "model.txt"))
        (staging / "metrics.json").write_text(json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False) + "\n")
        (staging / "config.json").write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n")
        predictions = test[key + ["entry_date", "exit_date", "y"]].copy()
        predictions["prediction"] = test_prediction
        predictions.to_csv(staging / "test_predictions.csv", index=False)
        require(not args.output.exists(), "output appeared during training; refusing replacement")
        os.rename(staging, args.output)
    print(json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
