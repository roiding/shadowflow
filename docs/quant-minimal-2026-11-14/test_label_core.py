from dataclasses import replace
from datetime import date, datetime, timedelta
from decimal import Decimal
import unittest

from label_core import Action, ActionCoverage, Price, SHANGHAI, make_label, factor_return, usable_for_fit_before


class LabelContractTests(unittest.TestCase):
    def setUp(self):
        self.days = [date.fromisoformat(d) for d in ("2026-11-02", "2026-11-03", "2026-11-04",
            "2026-11-05", "2026-11-06", "2026-11-09", "2026-11-10", "2026-11-11")]
        self.security = "SYNTHETIC.SZ"
        self.ready = datetime(2026, 11, 10, 16, 30, tzinfo=SHANGHAI)
        self.as_of = self.ready + timedelta(days=1)
        self.coverage = ActionCoverage(self.security, self.days[0], self.days[-1], self.ready, "fixture-v1", True)
        self.prices = {(self.security, d): Price(self.security, d, Decimal("10"), self.ready, "synthetic", contract_verified=True) for d in self.days}

    def label(self, actions=(), **kwargs):
        return make_label(self.security, self.days[0], self.days, self.prices, actions, self.coverage,
            dataset_as_of=self.as_of, dataset_id="test-fixture", **kwargs)

    def exit_price(self, price):
        key = (self.security, self.days[6])
        self.prices[key] = replace(self.prices[key], open=Decimal(price))

    def action(self, cash="0", multiplier="1", day_index=2, kind="cash_split"):
        return Action("event-1", self.security, self.days[day_index], self.ready,
            Decimal(cash), Decimal(multiplier), kind)

    def test_normal_return_and_five_intervals(self):
        self.exit_price("11")
        label = self.label()
        self.assertEqual((label.entry_date, label.exit_date), (self.days[1], self.days[6]))
        self.assertEqual(label.y, Decimal("0.1"))

    def test_one_interval_respects_t_plus_one(self):
        label = self.label(horizon=1)
        self.assertEqual((label.entry_date, label.exit_date), (self.days[1], self.days[2]))

    def test_cash_distribution_flat_wealth(self):
        self.exit_price("9")
        label = self.label([self.action(cash="1")])
        self.assertEqual(label.y, 0)
        self.assertEqual(label.raw_price_return, Decimal("-0.1"))

    def test_split_flat_wealth(self):
        self.exit_price("5")
        label = self.label([self.action(multiplier="2")])
        self.assertEqual(label.y, 0)
        self.assertEqual(label.raw_price_return, Decimal("-0.5"))

    def test_simultaneous_cash_and_split_use_old_shares(self):
        self.exit_price("4.5")
        label = self.label([self.action(cash="1", multiplier="2")])
        self.assertEqual(label.y, 0)
        self.assertEqual(label.cash_per_initial_share, 1)

    def test_cash_label_differs_from_exchange_reference_return(self):
        self.exit_price("9.9")
        self.assertEqual(self.label([self.action(cash="1")]).y, Decimal("0.09"))
        self.assertEqual(Decimal("9.9") / Decimal("9") - 1, Decimal("0.1"))

    def test_entry_day_distribution_not_owned(self):
        self.assertEqual(self.label([self.action(cash="1", day_index=1)]).y, 0)

    def test_exit_day_distribution_is_owned(self):
        self.exit_price("9")
        self.assertEqual(self.label([self.action(cash="1", day_index=6)]).y, 0)

    def test_cash_after_split_scales_with_owned_shares(self):
        self.exit_price("4.5")
        split = self.action(multiplier="2")
        cash = replace(self.action(cash="0.5", day_index=3), event_id="event-2")
        self.assertEqual(self.label([cash, split]).y, 0)

    def test_missing_exit_does_not_move_to_next_trading_day(self):
        del self.prices[(self.security, self.days[6])]
        label = self.label()
        self.assertEqual(label.exit_date, self.days[6])
        self.assertEqual(label.status, "missing_exit_price")
        self.assertIsNone(label.y)

    def test_empty_actions_require_positive_coverage_evidence(self):
        self.coverage = replace(self.coverage, complete=False)
        self.assertEqual(self.label().status, "corporate_actions_unverified")

    def test_adjusted_price_rejected_to_prevent_double_adjustment(self):
        key = (self.security, self.days[6])
        self.prices[key] = replace(self.prices[key], adjustment="qfq")
        self.assertEqual(self.label().status, "price_contract_unverified")

    def test_unsupported_rights_issue_not_silently_zeroed(self):
        label = self.label([self.action(kind="rights_issue")])
        self.assertEqual(label.status, "unsupported_corporate_action")
        self.assertIsNone(label.y)

    def test_duplicate_event_rejected(self):
        action = self.action(cash="1")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.label([action, action])

    def test_same_date_unmerged_events_rejected(self):
        action = self.action(cash="1")
        with self.assertRaisesRegex(ValueError, "same-day"):
            self.label([action, replace(action, event_id="event-2")])

    def test_factor_scale_invariance(self):
        kw = dict(entry_series_id="verified-v1", exit_series_id="verified-v1")
        self.assertEqual(factor_return(10, 5, 1, 2, **kw), 0)
        self.assertEqual(factor_return(10, 5, 100, 200, **kw), 0)

    def test_factor_vintage_mixing_rejected(self):
        with self.assertRaisesRegex(ValueError, "same verified"):
            factor_return(10, 5, 1, 2, entry_series_id="v1", exit_series_id="v2")

    def test_non_finite_or_zero_factor_rejected(self):
        for value in ["NaN", "Infinity", "0"]:
            with self.assertRaises(ValueError):
                factor_return(10, 5, value, 2, entry_series_id="v1", exit_series_id="v1")

    def test_purge_requires_label_ready_before_fold_boundary(self):
        label = self.label()
        self.assertFalse(usable_for_fit_before(label, self.ready - timedelta(hours=1)))
        self.assertTrue(usable_for_fit_before(label, self.ready + timedelta(days=1)))

    def test_cannot_train_on_future_response(self):
        key = (self.security, self.days[6])
        self.prices[key] = replace(self.prices[key], available_at=self.as_of + timedelta(days=1))
        self.assertEqual(self.label().status, "not_matured")

    def test_blocked_exit_keeps_valuation_label_and_execution_flag(self):
        key = (self.security, self.days[6])
        self.prices[key] = replace(self.prices[key], sell_at_open=False)
        label = self.label()
        self.assertEqual(label.status, "ok")
        self.assertIn("exit_blocked", label.execution_flags)
        self.assertEqual(label.exit_date, self.days[6])

    def test_unverified_currency_and_calendar_rejected(self):
        key = (self.security, self.days[6])
        self.prices[key] = replace(self.prices[key], currency="USD")
        self.assertEqual(self.label().status, "price_contract_unverified")
        self.days = self.days[::-1]
        with self.assertRaisesRegex(ValueError, "calendar"):
            self.label()


if __name__ == "__main__":
    unittest.main()
