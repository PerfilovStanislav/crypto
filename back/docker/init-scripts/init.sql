CREATE TABLE IF NOT EXISTS default.market_data
(
    symbol LowCardinality(String),
    timeframe Enum8(
          '1m' = 1
        , '3m' = 2
        , '5m' = 3
        , '15m' = 4
        , '30m' = 5
        , '1h' = 6
        , '2h' = 7
        , '4h' = 8
        , '1d' = 9
    ),
    timestamp DateTime,

    low Float64,
    open Float64,
    close Float64,
    high Float64,
    volume Float64
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (symbol, timeframe, timestamp)
SETTINGS index_granularity = 8192;
