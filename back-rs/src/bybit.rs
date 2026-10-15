//! Публичный kline API Bybit v5.

use anyhow::{bail, Context, Result};
use serde::Deserialize;

use crate::timeframe::Timeframe;

const KLINE_URL: &str = "https://api.bybit.com/v5/market/kline";

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Candle {
    pub timestamp_ms: i64,
    pub open: f64,
    pub high: f64,
    pub low: f64,
    pub close: f64,
    pub volume: f64,
}

#[derive(Deserialize)]
struct KlineResponse {
    #[serde(rename = "retCode")]
    ret_code: i64,
    #[serde(rename = "retMsg", default)]
    ret_msg: String,
    #[serde(default)]
    result: KlineResult,
}

#[derive(Deserialize, Default)]
struct KlineResult {
    #[serde(default)]
    list: Vec<Vec<String>>,
}

/// До 1000 свечей спота в `[start_ms, end_ms]`, новые первыми — как отдаёт Bybit.
pub async fn fetch_klines(
    http: &reqwest::Client,
    symbol: &str,
    timeframe: Timeframe,
    start_ms: i64,
    end_ms: i64,
) -> Result<Vec<Vec<String>>> {
    let start = start_ms.to_string();
    let end = end_ms.to_string();
    let resp = http
        .get(KLINE_URL)
        .query(&[
            ("category", "spot"),
            ("symbol", symbol),
            ("interval", timeframe.bybit_interval()),
            ("limit", "1000"),
            ("start", start.as_str()),
            ("end", end.as_str()),
        ])
        .send()
        .await?;

    let status = resp.status();
    if status != reqwest::StatusCode::OK {
        bail!("unexpected http status: {}", status.as_u16());
    }

    let body: KlineResponse = resp.json().await?;
    if body.ret_code != 0 {
        bail!(
            "bybit API returned error code {}: {}",
            body.ret_code,
            body.ret_msg
        );
    }
    Ok(body.result.list)
}

/// `[startTime, open, high, low, close, volume, turnover]` → [`Candle`].
pub fn parse_candle(raw: &[String]) -> Result<Candle> {
    if raw.len() < 6 {
        bail!("insufficient fields, got {} expected at least 6", raw.len());
    }
    let num = |i: usize, name: &str| -> Result<f64> {
        raw[i]
            .parse()
            .with_context(|| format!("invalid {name} {:?}", raw[i]))
    };
    Ok(Candle {
        timestamp_ms: raw[0]
            .parse()
            .with_context(|| format!("invalid timestamp {:?}", raw[0]))?,
        open: num(1, "open price")?,
        high: num(2, "high price")?,
        low: num(3, "low price")?,
        close: num(4, "close price")?,
        volume: num(5, "volume")?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_bybit_row() {
        let raw: Vec<String> = ["1700000000000", "1.5", "2", "1", "1.75", "100", "150"]
            .map(String::from)
            .to_vec();
        let c = parse_candle(&raw).unwrap();
        assert_eq!(c.timestamp_ms, 1_700_000_000_000);
        assert_eq!(
            (c.open, c.high, c.low, c.close, c.volume),
            (1.5, 2.0, 1.0, 1.75, 100.0)
        );
        assert!(parse_candle(&raw[..5]).is_err());
    }
}
