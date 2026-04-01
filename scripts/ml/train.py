#!/usr/bin/env python3
"""Train a logistic-regression model on klines from TimescaleDB.

Usage:
    python train.py --symbol BTCUSDT --interval 1h --out ../../models/btc_lr.json
    python train.py --symbol BTCUSDT --interval 1h --dsn postgresql://user:pw@localhost/quantix
"""

import argparse
import json
import os
import sys

import numpy as np
import pandas as pd
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import accuracy_score, precision_score, recall_score
from sklearn.preprocessing import StandardScaler
from sqlalchemy import create_engine, text

from features import FEATURE_NAMES, compute_features

DEFAULT_DSN = "postgresql://quantix:quantix_secret@localhost:5432/quantix"


def load_klines(dsn: str, symbol: str, interval: str) -> pd.DataFrame:
    engine = create_engine(dsn)
    query = text(
        """
        SELECT time AS open_time, open, high, low, close, volume
        FROM klines
        WHERE symbol = :symbol AND interval = :interval
        ORDER BY time ASC
        """
    )
    with engine.connect() as conn:
        df = pd.read_sql(query, conn, params={"symbol": symbol, "interval": interval})
    if df.empty:
        raise ValueError(f"No klines found for {symbol}/{interval}")
    df = df.set_index("open_time")
    return df


def train(
    df: pd.DataFrame,
    out_path: str,
    test_size: float = 0.2,
) -> None:
    feat = compute_features(df)
    X = feat[FEATURE_NAMES].values
    y = feat["y"].values

    # Chronological train/test split (no shuffling)
    split = int(len(X) * (1 - test_size))
    X_train, X_test = X[:split], X[split:]
    y_train, y_test = y[:split], y[split:]

    # Standardise
    scaler = StandardScaler()
    X_train_s = scaler.fit_transform(X_train)
    X_test_s = scaler.transform(X_test)

    # Train
    model = LogisticRegression(max_iter=500, random_state=42)
    model.fit(X_train_s, y_train)

    # Evaluate
    y_pred = model.predict(X_test_s)
    acc = accuracy_score(y_test, y_pred)
    prec = precision_score(y_test, y_pred, zero_division=0)
    rec = recall_score(y_test, y_pred, zero_division=0)

    print(f"\nModel evaluation (out-of-sample {split}:{len(X)}):")
    print(f"  Accuracy:  {acc:.4f}")
    print(f"  Precision: {prec:.4f}")
    print(f"  Recall:    {rec:.4f}")
    print(f"  Train set: {split} bars | Test set: {len(X) - split} bars")

    # Export weights
    weights = {
        "coefficients": model.coef_[0].tolist(),
        "intercept": float(model.intercept_[0]),
        "feature_names": FEATURE_NAMES,
        "scaler": {
            "mean": scaler.mean_.tolist(),
            "scale": scaler.scale_.tolist(),
        },
    }

    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    with open(out_path, "w") as f:
        json.dump(weights, f, indent=2)
    print(f"\nModel weights saved to: {out_path}")


def main():
    parser = argparse.ArgumentParser(description="Train LR model on klines")
    parser.add_argument("--symbol", default="BTCUSDT")
    parser.add_argument("--interval", default="1h")
    parser.add_argument("--dsn", default=DEFAULT_DSN)
    parser.add_argument("--out", default="../../models/btc_lr.json")
    parser.add_argument("--test-size", type=float, default=0.2)
    args = parser.parse_args()

    print(f"Loading klines for {args.symbol}/{args.interval} ...")
    try:
        df = load_klines(args.dsn, args.symbol, args.interval)
    except Exception as e:
        print(f"Error loading klines: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"Loaded {len(df)} bars. Training model ...")
    train(df, args.out, args.test_size)


if __name__ == "__main__":
    main()
