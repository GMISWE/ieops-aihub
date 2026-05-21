"""RRF fusion + score-scale helpers (no HTTP concerns; pure functions)."""
import os
from typing import Sequence

RRF_K_DEFAULT = int(os.getenv("RRF_K", "60"))


def rrf_fuse(
    rank_lists: Sequence[Sequence[int]],
    weights: Sequence[float],
    k: int = RRF_K_DEFAULT,
) -> dict[int, float]:
    """Reciprocal Rank Fusion.

    rank_lists[c] = ordered rowids from channel c (rank 0 == best)
    weights[c]    = float weight for channel c (>=0; 0 disables)
    Returns      = {rowid: fused_score}, higher == more relevant.
    """
    scores: dict[int, float] = {}
    for ranks, w in zip(rank_lists, weights):
        if w == 0.0:
            continue
        for r, rowid in enumerate(ranks):
            scores[rowid] = scores.get(rowid, 0.0) + w * (1.0 / (k + r))
    return scores


def score_max_theoretical(weights: Sequence[float], k: int = RRF_K_DEFAULT) -> float:
    """Upper bound on RRF fused score: best-rank-in-every-active-channel."""
    return sum(w for w in weights if w > 0) * (1.0 / k)
