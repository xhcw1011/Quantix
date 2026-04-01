"""Feature engineering for the ML trading strategy.

All features are computed from a DataFrame with columns:
  close, volume, (optionally: open, high, low)

Returns a DataFrame with feature columns and a binary label column 'y'.
"""

import numpy as np
import pandas as pd


FEATURE_NAMES = [
    "rsi",
    "macd_hist",
    "bb_pos",
    "ret_5",
    "ret_20",
    "vol_20",
    "vol_ratio",
]


def _ema(series: pd.Series, period: int) -> pd.Series:
    return series.ewm(span=period, adjust=False).mean()


def _rsi(close: pd.Series, period: int = 14) -> pd.Series:
    delta = close.diff()
    gain = delta.clip(lower=0)
    loss = -delta.clip(upper=0)
    avg_gain = gain.ewm(com=period - 1, min_periods=period).mean()
    avg_loss = loss.ewm(com=period - 1, min_periods=period).mean()
    rs = avg_gain / avg_loss.replace(0, np.nan)
    return 100 - (100 / (1 + rs))


def _macd(close: pd.Series, fast: int = 12, slow: int = 26, signal: int = 9):
    ema_fast = _ema(close, fast)
    ema_slow = _ema(close, slow)
    macd_line = ema_fast - ema_slow
    signal_line = _ema(macd_line, signal)
    histogram = macd_line - signal_line
    return macd_line, signal_line, histogram


def _bollinger(close: pd.Series, period: int = 20, std_dev: float = 2.0):
    mid = close.rolling(period).mean()
    std = close.rolling(period).std()
    upper = mid + std_dev * std
    lower = mid - std_dev * std
    return upper, mid, lower


def compute_features(df: pd.DataFrame) -> pd.DataFrame:
    """Compute feature matrix from kline DataFrame.

    Parameters
    ----------
    df : pd.DataFrame
        Must have 'close' and 'volume' columns, sorted ascending by time.

    Returns
    -------
    pd.DataFrame
        Feature columns + 'y' label (1 if next close > current close).
        NaN rows are dropped.
    """
    close = df["close"].astype(float)
    volume = df["volume"].astype(float)

    # ── Technical indicators ──────────────────────────────────────────────────
    rsi = _rsi(close, 14)

    _, _, macd_hist = _macd(close, 12, 26, 9)

    upper, _, lower = _bollinger(close, 20, 2.0)
    bb_range = (upper - lower).replace(0, np.nan)
    bb_pos = (close - lower) / bb_range

    # ── Price returns ─────────────────────────────────────────────────────────
    ret_5 = close.pct_change(5)
    ret_20 = close.pct_change(20)

    # ── Volatility ────────────────────────────────────────────────────────────
    log_ret = np.log(close / close.shift(1))
    vol_20 = log_ret.rolling(20).std()

    # ── Volume ratio ─────────────────────────────────────────────────────────
    vol_sma20 = volume.rolling(20).mean().replace(0, np.nan)
    vol_ratio = volume / vol_sma20

    # ── Label: 1 if next close > current close ────────────────────────────────
    y = (close.shift(-1) > close).astype(int)

    features = pd.DataFrame(
        {
            "rsi": rsi,
            "macd_hist": macd_hist,
            "bb_pos": bb_pos,
            "ret_5": ret_5,
            "ret_20": ret_20,
            "vol_20": vol_20,
            "vol_ratio": vol_ratio,
            "y": y,
        },
        index=df.index,
    )

    # Drop rows with NaN in any feature (warm-up period) or last row (no label)
    features = features.dropna()
    return features
