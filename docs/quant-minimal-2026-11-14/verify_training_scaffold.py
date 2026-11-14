"""Synthetic process test. No real stocks, no performance claims, no network.

Run with the Python interpreter containing requirements.txt dependencies.
Temporary features/models are removed; only verification.json is published.
"""

import csv
from datetime import date, timedelta
import json
from pathlib import Path
import random
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent


def write_csv(path, rows):
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)


def main():
    generator = random.Random(11)
    calendar, cursor = [], date(2024, 1, 1)
    while len(calendar) < 500:
        if cursor.weekday() < 5:
            calendar.append(cursor.isoformat())
        cursor += timedelta(days=1)
    cfg = json.loads((HERE / "config.json").read_text())
    cfg.update(train_start=calendar[25], validation_start=calendar[320],
               test_start=calendar[400], test_end=calendar[480],
               num_boost_round=12, early_stopping_rounds=3)
    features, labels = [], []
    for stock in range(25):
        prices = [100.0]
        for _ in calendar[1:]:
            prices.append(prices[-1] * (1 + generator.gauss(0, 0.003)))
        for i in range(25, 481):
            identity = dict(dataset_id="SYNTHETIC_FLOW_TEST_ONLY", security=f"{stock+1:06d}.SZ", signal_date=calendar[i])
            features.append(dict(identity, signal_at=calendar[i] + "T17:00:00+08:00",
                feature_available_at=calendar[i] + "T16:35:00+08:00", eligible_asof=1,
                ret_1d_adjusted=prices[i]/prices[i-1]-1,
                ret_5d_adjusted=prices[i]/prices[i-5]-1,
                ret_20d_adjusted=prices[i]/prices[i-20]-1,
                volatility_20d=0.003 + generator.random()/1000,
                log_amount=20+generator.random(), amount_ratio_20d=0.8+generator.random()*0.4,
                turnover_pct=1+generator.random(), amplitude_pct=1+generator.random()))
            labels.append(dict(identity, entry_date=calendar[i+1], exit_date=calendar[i+6], horizon=5,
                label_version=cfg["label_version"], action_version="synthetic-no-actions-v1", status="ok",
                y=prices[i+6]/prices[i+1]-1, label_ready_at=calendar[i+6]+"T16:35:00+08:00"))
    result = {"kind": "synthetic_training_flow_only", "no_real_training": True, "checks": {}}
    with tempfile.TemporaryDirectory(prefix="shadowflow-quant-smoke-") as temporary:
        root = Path(temporary)
        write_csv(root / "features.csv", features)
        write_csv(root / "labels.csv", labels)
        (root / "calendar.json").write_text(json.dumps({"trading_dates": calendar, "source": "synthetic weekdays, not an exchange calendar"}))
        (root / "config.json").write_text(json.dumps(cfg))
        command = [sys.executable, str(HERE / "train_lightgbm.py"), "--features", str(root / "features.csv"),
            "--labels", str(root / "labels.csv"), "--calendar", str(root / "calendar.json"),
            "--config", str(root / "config.json"), "--as-of", calendar[-1]+"T20:00:00+08:00", "--output"]
        try:
            valid = subprocess.run(command+[str(root / "valid-run")], text=True, capture_output=True, timeout=60)
        except subprocess.TimeoutExpired as exc:
            detail = exc.stderr or b""
            if isinstance(detail, bytes):
                detail = detail.decode(errors="replace")
            raise RuntimeError("Training process timed out; last progress:\n" + detail[-2000:]) from exc
        if valid.returncode:
            raise RuntimeError(valid.stderr[-2000:])
        metrics = json.loads((root / "valid-run" / "metrics.json").read_text())
        assert metrics["availability_mode"] == "strict_vintage"
        required = {"model.txt", "metrics.json", "config.json", "test_predictions.csv"}
        assert required <= {p.name for p in (root / "valid-run").iterdir()}
        result["checks"]["valid_inputs_train_and_publish"] = True
        result["versions"] = metrics["versions"]
        result["partition_dates"] = {"train": metrics["train_dates"], "validation": metrics["validation"]["signal_dates"], "test": metrics["test"]["signal_dates"]}
        # These failures test input contracts, not the trained model's return.
        original = features[0]["feature_available_at"]
        features[0]["feature_available_at"] = calendar[-1]+"T20:00:00+08:00"
        write_csv(root / "features.csv", features)
        invalid = subprocess.run(command+[str(root / "future-feature")], text=True, capture_output=True, timeout=30)
        assert invalid.returncode != 0 and "future feature" in invalid.stderr
        assert not (root / "future-feature").exists()
        result["checks"]["future_feature_rejected"] = True
        features[0]["feature_available_at"] = original
        write_csv(root / "features.csv", features)
        original_exit = labels[0]["exit_date"]
        labels[0]["exit_date"] = calendar[32]  # should be calendar[31]
        write_csv(root / "labels.csv", labels)
        invalid = subprocess.run(command+[str(root / "wrong-horizon")], text=True, capture_output=True, timeout=30)
        assert invalid.returncode != 0 and "exchange-calendar horizon" in invalid.stderr
        assert not (root / "wrong-horizon").exists()
        result["checks"]["wrong_horizon_rejected"] = True
        labels[0]["exit_date"] = original_exit
        for label in labels:
            label["label_ready_at"] = calendar[-1]+"T16:35:00+08:00"
        write_csv(root / "labels.csv", labels)
        invalid = subprocess.run(command+[str(root / "late-vintage")], text=True, capture_output=True, timeout=30)
        assert invalid.returncode != 0 and "too few train dates" in invalid.stderr
        assert not (root / "late-vintage").exists()
        result["checks"]["strict_mode_rejects_later_known_vintages"] = True
        cfg["availability_mode"] = "reconstructed_research"
        (root / "config.json").write_text(json.dumps(cfg))
        retrospective = subprocess.run(command+[str(root / "reconstructed-run")], text=True, capture_output=True, timeout=60)
        if retrospective.returncode:
            raise RuntimeError(retrospective.stderr[-2000:])
        reconstructed_metrics = json.loads((root / "reconstructed-run" / "metrics.json").read_text())
        assert reconstructed_metrics["availability_mode"] == "reconstructed_research"
        assert "not evidence" in reconstructed_metrics["vintage_note"]
        result["checks"]["reconstructed_mode_explicitly_marked"] = True
    (HERE / "training-verification.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
