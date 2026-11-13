from dataclasses import dataclass
from datetime import date, timedelta
import json
import math


@dataclass(frozen=True)
class DirectionObservation:
    code: str
    sealed_codes: frozenset[str]
    broken_count: int
    down_count: int
    elapsed_trading_minutes: float
    known_theme_members: int
    known_universe_members: int
    sealed_known_universe_members: int


@dataclass(frozen=True)
class HistoricalEdge:
    co_seal_dates: frozenset[date]
    candidate_seal_days: int
    anchor_seal_days: int
    complete_event_days: int


def direction_components(observation):
    sealed_count = len(observation.sealed_codes)
    if not (
        0 <= sealed_count <= observation.known_theme_members
        <= observation.known_universe_members
        and observation.known_universe_members > 0
        and sealed_count <= observation.sealed_known_universe_members
        <= observation.known_universe_members
        and sealed_count >= observation.sealed_known_universe_members
        + observation.known_theme_members
        - observation.known_universe_members
        and observation.broken_count >= 0
        and observation.down_count >= 0
        and sealed_count + observation.broken_count + observation.down_count
        <= observation.known_theme_members
        and observation.elapsed_trading_minutes >= 0
    ):
        raise ValueError("invalid direction observation")
    expected_sealed = (
        observation.sealed_known_universe_members
        * observation.known_theme_members
        / observation.known_universe_members
    )
    if sealed_count < 2:
        return {"strength": 0.0, "expected_sealed": expected_sealed}
    breadth = sealed_count / (sealed_count + 3)
    concentration = max(
        0.0, (sealed_count - expected_sealed) / (sealed_count + expected_sealed)
    )
    seal_quality = sealed_count / (
        sealed_count + 0.5 * observation.broken_count + observation.down_count
    )
    decay = math.exp(-observation.elapsed_trading_minutes / 90)
    freshness = decay + (1 - decay) * sealed_count / (sealed_count + 5)
    return {
        "strength": breadth * concentration * seal_quality * freshness,
        "breadth": breadth,
        "concentration": concentration,
        "seal_quality": seal_quality,
        "freshness": freshness,
        "expected_sealed": expected_sealed,
    }


def historical_common_days(
    edges, window_start, window_end, minimum_common_days=2
):
    if minimum_common_days < 2:
        raise ValueError("historical links require at least two common days")
    if window_start > window_end:
        raise ValueError("window start must not be after window end")
    dates = set()
    for edge in edges:
        window_dates = {
            day for day in edge.co_seal_dates if window_start <= day <= window_end
        }
        if len(window_dates) >= minimum_common_days:
            dates.update(window_dates)
    return frozenset(dates)


def historical_link_strength(
    edges, window_start, window_end, minimum_common_days=2
):
    common_days = len(
        historical_common_days(
            edges, window_start, window_end, minimum_common_days
        )
    )
    return common_days / (common_days + 5)


def phi_diagnostic(edge):
    co_seal_days = len(edge.co_seal_dates)
    if not (
        0 <= co_seal_days <= min(edge.candidate_seal_days, edge.anchor_seal_days)
        and max(edge.candidate_seal_days, edge.anchor_seal_days)
        <= edge.complete_event_days
        and edge.complete_event_days > 0
        and co_seal_days
        >= edge.candidate_seal_days
        + edge.anchor_seal_days
        - edge.complete_event_days
    ):
        raise ValueError("invalid historical edge")
    denominator = math.sqrt(
        edge.candidate_seal_days
        * edge.anchor_seal_days
        * (edge.complete_event_days - edge.candidate_seal_days)
        * (edge.complete_event_days - edge.anchor_seal_days)
    )
    if denominator == 0:
        return 0.0
    return max(
        0.0,
        (
            co_seal_days * edge.complete_event_days
            - edge.candidate_seal_days * edge.anchor_seal_days
        )
        / denominator,
    )


def combine_direction_evidence(contributions, broken_today=False):
    contributions = sorted(contributions, key=lambda item: (-item[0], item[1].code))
    member_evidence = {}
    evidence = []
    for contribution, observation, historical_strength in contributions:
        if not 0 <= contribution <= 1:
            raise ValueError("direction evidence must be between zero and one")
        if contribution == 0:
            continue
        if not observation.sealed_codes:
            raise ValueError("positive direction evidence requires sealed members")
        unit_evidence = 1 - (1 - contribution) ** (
            1 / len(observation.sealed_codes)
        )
        for code in observation.sealed_codes:
            member_evidence[code] = max(
                member_evidence.get(code, 0.0), unit_evidence
            )
        evidence.append(
            {
                "direction": observation.code,
                "single_direction_score": contribution * 100,
                "historical_link": historical_strength,
                "member_unit_evidence": unit_evidence,
            }
        )
    remaining = math.prod(
        1 - member_evidence[code] for code in sorted(member_evidence)
    )
    penalty = 0.8 if broken_today else 1.0
    return {"score": 100 * (1 - remaining) * penalty, "evidence": evidence}


def candidate_priority(
    observations,
    theme_links,
    window_start,
    window_end,
    broken_today=False,
    minimum_common_days=2,
):
    if minimum_common_days < 2:
        raise ValueError("historical links require at least two common days")
    if len({observation.code for observation in observations}) != len(observations):
        raise ValueError("direction codes must be unique")
    contributions = []
    for observation in observations:
        historical_strength = historical_link_strength(
            theme_links.get(observation.code, []),
            window_start,
            window_end,
            minimum_common_days,
        )
        strength = direction_components(observation)["strength"]
        contributions.append(
            (
                strength * (0.6 + 0.4 * historical_strength),
                observation,
                historical_strength,
            )
        )
    return combine_direction_evidence(contributions, broken_today)


def date_span(start, count):
    return frozenset(start + timedelta(days=offset) for offset in range(count))


def run_examples():
    window_start = date(2025, 11, 13)
    window_end = date(2026, 11, 12)
    robotics = DirectionObservation(
        "robotics",
        frozenset({"r1", "r2", "r3", "r4", "r5", "r6"}),
        1,
        0,
        15,
        100,
        3000,
        80,
    )
    optical = DirectionObservation(
        "optical",
        frozenset({"o1", "o2", "o3", "o4", "o5"}),
        0,
        0,
        5,
        60,
        3000,
        80,
    )
    directions = [robotics, optical]

    two_day_edge = HistoricalEdge(
        date_span(date(2026, 1, 5), 2), 6, 8, 200
    )
    four_day_edge = HistoricalEdge(
        date_span(date(2026, 2, 2), 4), 6, 8, 200
    )
    five_day_edge = HistoricalEdge(
        date_span(date(2026, 3, 2), 5), 6, 8, 200
    )
    ten_day_edge = HistoricalEdge(
        date_span(date(2026, 4, 1), 10), 16, 18, 200
    )
    twenty_day_edge = HistoricalEdge(
        date_span(date(2026, 5, 4), 20), 26, 28, 200
    )
    frequent_self_edge = HistoricalEdge(
        four_day_edge.co_seal_dates, 60, 8, 200
    )
    frequent_anchor_edge = HistoricalEdge(
        four_day_edge.co_seal_dates, 6, 100, 200
    )
    duplicate_member_edge = HistoricalEdge(
        four_day_edge.co_seal_dates, 7, 8, 200
    )
    overlapping_member_edge = HistoricalEdge(
        date_span(date(2026, 5, 10), 4) | frozenset({date(2026, 2, 5)}),
        7,
        10,
        200,
    )
    one_day_edge = HistoricalEdge(
        date_span(date(2026, 7, 1), 1), 6, 8, 200
    )
    short_member_edge = HistoricalEdge(
        date_span(date(2026, 7, 3), 1), 7, 8, 200
    )
    other_single_day_edge = HistoricalEdge(
        date_span(date(2026, 7, 9), 1), 7, 8, 200
    )
    outside_edge = HistoricalEdge(
        frozenset(
            {
                window_start - timedelta(days=2),
                window_start - timedelta(days=1),
            }
        ),
        6,
        8,
        200,
    )
    partly_outside_edge = HistoricalEdge(
        four_day_edge.co_seal_dates
        | frozenset(
            {
                window_start - timedelta(days=2),
                window_end + timedelta(days=2),
            }
        ),
        6,
        8,
        200,
    )

    repeated_result = candidate_priority(
        [robotics],
        {"robotics": [four_day_edge]},
        window_start,
        window_end,
    )
    frequent_self_result = candidate_priority(
        [robotics],
        {"robotics": [frequent_self_edge]},
        window_start,
        window_end,
    )
    frequent_anchor_result = candidate_priority(
        [robotics],
        {"robotics": [frequent_anchor_edge]},
        window_start,
        window_end,
    )
    theme_only_result = candidate_priority(
        [robotics], {"robotics": []}, window_start, window_end
    )
    duplicate_member_result = candidate_priority(
        [robotics],
        {"robotics": [four_day_edge, duplicate_member_edge]},
        window_start,
        window_end,
    )
    overlapping_member_result = candidate_priority(
        [robotics],
        {"robotics": [four_day_edge, overlapping_member_edge]},
        window_start,
        window_end,
    )
    cross_result = candidate_priority(
        directions,
        {"robotics": [four_day_edge], "optical": []},
        window_start,
        window_end,
    )
    alias = DirectionObservation(
        "robotics_alias",
        robotics.sealed_codes,
        1,
        0,
        15,
        100,
        3000,
        80,
    )
    duplicate_result = candidate_priority(
        [robotics, alias],
        {"robotics": [four_day_edge], "robotics_alias": [four_day_edge]},
        window_start,
        window_end,
    )
    one_day_result = candidate_priority(
        [robotics], {"robotics": [one_day_edge]}, window_start, window_end
    )
    short_member_result = candidate_priority(
        [robotics],
        {"robotics": [four_day_edge, short_member_edge]},
        window_start,
        window_end,
    )
    two_single_day_result = candidate_priority(
        [robotics],
        {"robotics": [one_day_edge, other_single_day_edge]},
        window_start,
        window_end,
    )
    outside_result = candidate_priority(
        [robotics], {"robotics": [outside_edge]}, window_start, window_end
    )
    partly_outside_result = candidate_priority(
        [robotics],
        {"robotics": [partly_outside_edge]},
        window_start,
        window_end,
    )

    natural_breadth = DirectionObservation(
        "broad_label",
        frozenset(f"b{index}" for index in range(40)),
        0,
        0,
        0,
        1500,
        3000,
        80,
    )
    broken_direction = DirectionObservation(
        "robotics", robotics.sealed_codes, 3, 1, 15, 100, 3000, 80
    )
    aged_direction = DirectionObservation(
        "robotics", robotics.sealed_codes, 1, 0, 135, 100, 3000, 80
    )
    overlap_first = DirectionObservation(
        "overlap_first", frozenset({"a1", "a2"}), 0, 0, 0, 2, 10, 3
    )
    overlap_second = DirectionObservation(
        "overlap_second", frozenset({"a2", "a3"}), 0, 0, 0, 2, 10, 3
    )
    overlap_third = DirectionObservation(
        "overlap_third", frozenset({"a1", "a2", "a3"}), 0, 0, 0, 3, 10, 3
    )
    overlap_base = [(0.30, overlap_first, 0), (0.25, overlap_second, 0)]
    overlap_extended = [*overlap_base, (0.28, overlap_third, 0)]

    strength_two = historical_link_strength(
        [two_day_edge], window_start, window_end
    )
    strength_five = historical_link_strength(
        [five_day_edge], window_start, window_end
    )
    strength_ten = historical_link_strength(
        [ten_day_edge], window_start, window_end
    )
    strength_twenty = historical_link_strength(
        [twenty_day_edge], window_start, window_end
    )
    assert strength_two < strength_five < strength_ten < strength_twenty
    score_two = candidate_priority(
        [robotics],
        {"robotics": [two_day_edge]},
        window_start,
        window_end,
    )["score"]
    score_five = candidate_priority(
        [robotics],
        {"robotics": [five_day_edge]},
        window_start,
        window_end,
    )["score"]
    score_ten = candidate_priority(
        [robotics],
        {"robotics": [ten_day_edge]},
        window_start,
        window_end,
    )["score"]
    score_twenty = candidate_priority(
        [robotics],
        {"robotics": [twenty_day_edge]},
        window_start,
        window_end,
    )["score"]
    assert score_two < score_five < score_ten < score_twenty
    assert len(
        historical_common_days(
            [four_day_edge, duplicate_member_edge], window_start, window_end
        )
    ) == 4
    assert len(
        historical_common_days(
            [four_day_edge, overlapping_member_edge],
            window_start,
            window_end,
        )
    ) == 8
    assert len(
        historical_common_days(
            [four_day_edge, short_member_edge], window_start, window_end
        )
    ) == 4
    assert len(
        historical_common_days([outside_edge], window_start, window_end)
    ) == 0
    assert len(
        historical_common_days(
            [partly_outside_edge], window_start, window_end
        )
    ) == 4
    assert math.isclose(
        frequent_self_result["score"], repeated_result["score"]
    )
    assert math.isclose(
        frequent_anchor_result["score"], repeated_result["score"]
    )
    assert phi_diagnostic(frequent_self_edge) < phi_diagnostic(four_day_edge)
    assert duplicate_member_result["score"] == repeated_result["score"]
    assert overlapping_member_result["score"] > repeated_result["score"]
    assert one_day_result["score"] == theme_only_result["score"]
    assert two_single_day_result["score"] == theme_only_result["score"]
    assert short_member_result["score"] == repeated_result["score"]
    assert outside_result["score"] == theme_only_result["score"]
    assert partly_outside_result["score"] == repeated_result["score"]
    assert theme_only_result["score"] > 0
    assert duplicate_result["score"] == repeated_result["score"]
    assert cross_result["score"] > repeated_result["score"]
    assert cross_result == candidate_priority(
        list(reversed(directions)),
        {"optical": [], "robotics": [four_day_edge]},
        window_start,
        window_end,
    )
    assert repeated_result == candidate_priority(
        [robotics],
        {"robotics": [four_day_edge, one_day_edge]},
        window_start,
        window_end,
    )
    assert math.isclose(
        candidate_priority(
            [robotics],
            {"robotics": [four_day_edge]},
            window_start,
            window_end,
            minimum_common_days=5,
        )["score"],
        theme_only_result["score"],
    )
    assert combine_direction_evidence(overlap_extended)["score"] >= (
        combine_direction_evidence(overlap_base)["score"]
    )
    assert combine_direction_evidence(overlap_extended) == (
        combine_direction_evidence(list(reversed(overlap_extended)))
    )
    assert direction_components(natural_breadth)["strength"] == 0
    assert direction_components(broken_direction)["strength"] < (
        direction_components(robotics)["strength"]
    )
    assert direction_components(aged_direction)["strength"] < (
        direction_components(robotics)["strength"]
    )
    assert candidate_priority(
        [robotics],
        {"robotics": [four_day_edge]},
        window_start,
        window_end,
        broken_today=True,
    )["score"] < repeated_result["score"]
    assert all(
        0 <= result["score"] <= 100
        for result in [
            repeated_result,
            frequent_self_result,
            theme_only_result,
            cross_result,
            duplicate_result,
        ]
    )

    return {
        "synthetic_examples_only": True,
        "ranking_online_quote_requests": 0,
        "checks": [
            "historical strength rises strictly at two, five, ten and twenty common days",
            "higher own limit-up frequency with the same common days cannot reduce the score",
            "higher anchor limit-up frequency with the same common days cannot reduce the score",
            "phi changes only the diagnostic and never the score",
            "multiple members on the same days cannot double-count common dates",
            "an edge with only one common day adds no historical strength",
            "two one-day member edges cannot combine into eligible evidence",
            "an ineligible one-day member edge cannot contribute its date",
            "dates outside the selection window add no historical strength",
            "a direction can combine overlapping eligible member dates",
            "different historical themes do not create a strong edge",
            "theme-only candidates remain eligible",
            "identical sealed members cannot double-count theme labels",
            "distinct directions can increase cross-direction priority",
            "direction input order does not change the result",
            "ineligible additional edges do not reduce a stronger link",
            "raising the common-day threshold removes only the historical bonus",
            "an additional overlapping direction cannot reduce priority",
            "natural broad-label coverage has no excess-concentration score",
            "broken boards and limit-downs reduce direction strength",
            "time without a new seal reduces direction strength",
            "a currently broken candidate receives a bounded penalty",
            "scores stay between zero and one hundred",
        ],
        "scores": {
            "common_days_two": round(
                score_two, 4
            ),
            "common_days_five": round(
                score_five, 4
            ),
            "common_days_ten": round(
                score_ten, 4
            ),
            "common_days_twenty": round(
                score_twenty, 4
            ),
            "four_common_days": round(repeated_result["score"], 4),
            "frequent_self_same_days": round(
                frequent_self_result["score"], 4
            ),
            "theme_only": round(theme_only_result["score"], 4),
            "two_distinct_directions": round(cross_result["score"], 4),
            "duplicated_theme_same_members": round(
                duplicate_result["score"], 4
            ),
            "one_common_day": round(one_day_result["score"], 4),
            "two_single_day_edges": round(
                two_single_day_result["score"], 4
            ),
            "outside_window": round(outside_result["score"], 4),
        },
        "historical_strength": {
            "common_days_two": round(strength_two, 4),
            "common_days_five": round(strength_five, 4),
            "common_days_ten": round(strength_ten, 4),
            "common_days_twenty": round(strength_twenty, 4),
            "four_common_days": round(
                historical_link_strength(
                    [four_day_edge], window_start, window_end
                ),
                4,
            ),
        },
        "phi_diagnostic_only": {
            "four_common_days": round(phi_diagnostic(four_day_edge), 4),
            "frequent_self_same_days": round(
                phi_diagnostic(frequent_self_edge), 4
            ),
            "frequent_anchor_same_days": round(
                phi_diagnostic(frequent_anchor_edge), 4
            ),
        },
        "direction_components": {
            observation.code: direction_components(observation)
            for observation in directions
        },
    }


if __name__ == "__main__":
    print(json.dumps(run_examples(), ensure_ascii=False, indent=2))
