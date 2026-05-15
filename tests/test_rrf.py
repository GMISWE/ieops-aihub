from search import rrf_fuse, RRF_K_DEFAULT


def test_empty_channels():
    assert rrf_fuse([], []) == {}


def test_single_channel_orders_by_input():
    out = rrf_fuse([[10, 20, 30]], [1.0])
    assert out[10] > out[20] > out[30]


def test_two_channels_overlap_doubles_contribution():
    out = rrf_fuse([[10, 20], [10, 30]], [1.0, 1.0])
    # rowid 10 is rank-0 in both → score = 2 * 1/(60+0)
    # rowid 20 is rank-1 in one  → score = 1 * 1/(60+1)
    # rowid 30 is rank-1 in one  → score = 1 * 1/(60+1)
    assert out[10] == 2.0 / 60.0
    assert abs(out[20] - 1.0 / 61.0) < 1e-9
    assert abs(out[30] - 1.0 / 61.0) < 1e-9
    assert out[10] > out[20]


def test_weight_zero_disables_channel():
    out = rrf_fuse([[10, 20], [30, 40]], [1.0, 0.0])
    assert 30 not in out
    assert 40 not in out


def test_custom_k():
    out = rrf_fuse([[10]], [1.0], k=10)
    assert out[10] == 1.0 / 10.0


def test_max_theoretical_three_default_channels():
    out = rrf_fuse([[10], [10], [10]], [1.0, 1.0, 1.0])
    assert abs(out[10] - 3.0 / 60.0) < 1e-9
