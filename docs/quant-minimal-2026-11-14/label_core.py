"""Reference labels: next open -> h trading intervals later, cash not reinvested.

Prices must be verified, unadjusted prices in one currency. Cash entitlements
are recognised on the ex-date as cash/receivables. This is a gross wealth label,
not an assertion that every opening auction can be filled. No network or DB I/O.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass
from datetime import date, datetime
from decimal import Decimal
from typing import Mapping, Sequence
from zoneinfo import ZoneInfo

SHANGHAI = ZoneInfo("Asia/Shanghai")
VERSION = "cash-nav-next-open-v1"


def number(value: Decimal | str | int | float) -> Decimal:
    result = Decimal(str(value))
    if not result.is_finite():
        raise ValueError("non-finite number")
    return result


def aware(value: datetime) -> datetime:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError("timestamps must carry a timezone")
    return value


@dataclass(frozen=True)
class Price:
    security: str
    trade_date: date
    open: Decimal
    available_at: datetime
    provider: str
    adjustment: str = "none"
    currency: str = "CNY"
    contract_verified: bool = False
    # These flags are for a separate execution simulator, not future filters
    # on the signal universe or on the supervised learning sample.
    buy_at_open: bool | None = None
    sell_at_open: bool | None = None


@dataclass(frozen=True)
class Action:
    event_id: str
    security: str
    ex_date: date
    available_at: datetime
    cash_per_old_share: Decimal = Decimal("0")
    share_multiplier: Decimal = Decimal("1")
    kind: str = "cash_split"
    # The adapter must combine same-day cash and share distributions into
    # one event, both denominated per share held before that ex-date.


@dataclass(frozen=True)
class ActionCoverage:
    security: str
    from_date: date
    through_date: date
    verified_at: datetime
    version: str
    complete: bool


@dataclass(frozen=True)
class Label:
    security: str
    signal_date: date
    horizon: int
    entry_date: date | None
    exit_date: date | None
    status: str
    dataset_id: str
    action_version: str
    label_version: str = VERSION
    y: Decimal | None = None
    raw_price_return: Decimal | None = None
    shares_per_initial_share: Decimal | None = None
    cash_per_initial_share: Decimal | None = None
    label_ready_at: datetime | None = None
    execution_flags: tuple[str, ...] = ()

    def jsonable(self) -> dict:
        row = asdict(self)
        for key, value in row.items():
            if isinstance(value, (date, datetime)):
                row[key] = value.isoformat()
            elif isinstance(value, Decimal):
                row[key] = str(value)
        return row


def make_label(
    security: str,
    signal_date: date,
    calendar: Sequence[date],
    prices: Mapping[tuple[str, date], Price],
    actions: Sequence[Action],
    coverage: ActionCoverage,
    *,
    dataset_as_of: datetime,
    dataset_id: str,
    horizon: int = 5,
) -> Label:
    """No shifting across missing prices; h=1 exits the day AFTER entry.

    A complete ActionCoverage record is mandatory even when actions=[];
    an empty endpoint response is not proof that no corporate action occurred.
    """
    aware(dataset_as_of)
    if not dataset_id or not security or not coverage.version:
        raise ValueError("security, dataset_id and action version are required")
    if isinstance(horizon, bool) or not isinstance(horizon, int) or horizon < 1:
        raise ValueError("horizon must be a positive integer")
    if list(calendar) != sorted(set(calendar)) or signal_date not in calendar:
        raise ValueError("calendar must be sorted, unique, and contain the signal day")
    entry_index = calendar.index(signal_date) + 1
    exit_index = entry_index + horizon
    entry_date = calendar[entry_index] if entry_index < len(calendar) else None
    exit_date = calendar[exit_index] if exit_index < len(calendar) else None
    base = dict(security=security, signal_date=signal_date, horizon=horizon,
                entry_date=entry_date, exit_date=exit_date, dataset_id=dataset_id,
                action_version=coverage.version)

    def invalid(status: str) -> Label:
        return Label(**base, status=status)

    if entry_date is None or exit_date is None:
        return invalid("calendar_does_not_cover_target")
    if exit_date > dataset_as_of.astimezone(SHANGHAI).date():
        return invalid("not_matured")
    if (coverage.security != security or not coverage.complete
            or coverage.from_date > entry_date or coverage.through_date < exit_date
            or aware(coverage.verified_at) > dataset_as_of):
        return invalid("corporate_actions_unverified")
    entry = prices.get((security, entry_date))
    exit_ = prices.get((security, exit_date))
    if entry is None:
        return invalid("missing_entry_price")
    if exit_ is None:
        return invalid("missing_exit_price")
    for bar, expected_date in ((entry, entry_date), (exit_, exit_date)):
        if bar.security != security or bar.trade_date != expected_date:
            raise ValueError("price identity/date differs from its key")
        if bar.adjustment != "none" or not bar.contract_verified or bar.currency != "CNY":
            return invalid("price_contract_unverified")
        if aware(bar.available_at) > dataset_as_of:
            return invalid("not_matured")
        if number(bar.open) <= 0:
            return invalid("invalid_open_price")
    applicable = [a for a in actions if a.security == security
                  and entry_date < a.ex_date <= exit_date]
    # Entry is after that day's ex-right adjustment. Exit is also after that
    # day's adjustment, so entry-day actions are excluded, exit-day included.
    if len({a.event_id for a in applicable}) != len(applicable):
        raise ValueError("duplicate corporate action event_id")
    if len({a.ex_date for a in applicable}) != len(applicable):
        raise ValueError("combine same-day actions per old share before labelling")
    shares, cash = Decimal("1"), Decimal("0")
    ready_at = max(entry.available_at, exit_.available_at, coverage.verified_at)
    for action in sorted(applicable, key=lambda item: item.ex_date):
        if action.kind != "cash_split":
            return invalid("unsupported_corporate_action")
        if aware(action.available_at) > dataset_as_of:
            return invalid("not_matured")
        dividend, multiplier = number(action.cash_per_old_share), number(action.share_multiplier)
        if dividend < 0 or multiplier <= 0:
            raise ValueError("invalid cash/share multiplier")
        cash += shares * dividend
        shares *= multiplier
        ready_at = max(ready_at, action.available_at)
    entry_price, exit_price = number(entry.open), number(exit_.open)
    flags = []
    if entry.buy_at_open is not True:
        flags.append("entry_fill_unknown" if entry.buy_at_open is None else "entry_blocked")
    if exit_.sell_at_open is not True:
        flags.append("exit_fill_unknown" if exit_.sell_at_open is None else "exit_blocked")
    return Label(**base, status="ok", y=(shares * exit_price + cash) / entry_price - 1,
                 raw_price_return=exit_price / entry_price - 1,
                 shares_per_initial_share=shares, cash_per_initial_share=cash,
                 label_ready_at=ready_at, execution_flags=tuple(flags))


def factor_return(entry_price, exit_price, entry_factor, exit_factor, *,
                  entry_series_id: str, exit_series_id: str) -> Decimal:
    """Auxiliary multiplicative-adjustment return, NOT cash-ledger return.

    Series IDs bind provider, recipe and immutable snapshot. Never add the
    cash dividend again to this result. Vendor factors need separate checks.
    """
    if not entry_series_id or entry_series_id != exit_series_id:
        raise ValueError("factor endpoints must use the same verified series")
    p0, p1, a0, a1 = map(number, (entry_price, exit_price, entry_factor, exit_factor))
    if min(p0, p1, a0, a1) <= 0:
        raise ValueError("positive prices/factors required")
    return p1 * a1 / (p0 * a0) - 1


def usable_for_fit_before(label: Label, cutoff: datetime) -> bool:
    aware(cutoff)
    return (label.status == "ok" and label.exit_date is not None
            and label.exit_date < cutoff.astimezone(SHANGHAI).date()
            and label.label_ready_at is not None and label.label_ready_at < cutoff)


if __name__ == "__main__":
    import json
    days = [date.fromisoformat(d) for d in (
        "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05",
        "2026-11-06", "2026-11-09", "2026-11-10", "2026-11-11")]
    ready = datetime(2026, 11, 10, 16, 30, tzinfo=SHANGHAI)
    security = "SYNTHETIC.SZ"
    prices = {(security, days[1]): Price(security, days[1], Decimal("10"), ready, "synthetic", contract_verified=True),
              (security, days[6]): Price(security, days[6], Decimal("5"), ready, "synthetic", contract_verified=True)}
    coverage = ActionCoverage(security, days[0], days[-1], ready, "synthetic-v1", True)
    action = Action("split-1", security, days[2], ready, share_multiplier=Decimal("2"))
    result = make_label(security, days[0], days, prices, [action], coverage,
                        dataset_as_of=ready, dataset_id="synthetic-demo", horizon=5)
    print(json.dumps(result.jsonable(), ensure_ascii=False, indent=2))
