//! ClickHouse (`default.market_data`) и докачка свечей с Bybit.

use std::time::Duration;

use anyhow::{bail, Context, Result};
use chrono::{DateTime, Months, Utc};
use clickhouse::{Client, Row};
use serde::{Deserialize, Serialize};
use tracing::{info, warn};

use crate::bybit;
use crate::config::DbConfig;
use crate::source::Quotes;
use crate::timeframe::Timeframe;

/// 1980-01-01: `max(timestamp)` по пустой выборке возвращает 1970-01-01.
const EMPTY_TABLE_THRESHOLD: u32 = 315_532_800;

/// Таблица свечей, как в Go-версии.
pub const MARKET_DATA_TABLE: &str = "default.market_data";

pub struct Db {
    client: Client,
    table: String,
}

#[derive(Row, Serialize)]
struct MarketDataRow {
    symbol: String,
    timeframe: Timeframe,
    timestamp: u32,
    low: f64,
    open: f64,
    close: f64,
    high: f64,
    volume: f64,
}

#[derive(Row, Deserialize)]
struct QuoteRow {
    timestamp: u32,
    low: f64,
    open: f64,
    close: f64,
    high: f64,
    volume: f64,
}

#[derive(Row, Deserialize)]
struct TimeRange {
    min: u32,
    max: u32,
}

impl Db {
    /// Клиент работает по HTTP-интерфейсу ClickHouse (обычно порт 8123).
    pub async fn connect(cfg: &DbConfig) -> Result<Self> {
        if cfg.port == 9000 {
            bail!("port 9000 is the native ClickHouse protocol; use the HTTP port (8123)");
        }
        let client = Client::default()
            .with_url(format!("http://{}:{}", cfg.host, cfg.port))
            .with_user(&cfg.user)
            .with_password(&cfg.password)
            .with_database(&cfg.database);

        client
            .query("SELECT 1")
            .execute()
            .await
            .context("failed to ping clickhouse server")?;

        Ok(Self {
            client,
            table: MARKET_DATA_TABLE.to_owned(),
        })
    }

    /// Другая таблица с той же схемой (для интеграционных тестов).
    pub fn with_table(mut self, table: impl Into<String>) -> Self {
        self.table = table.into();
        self
    }

    pub fn client(&self) -> &Client {
        &self.client
    }

    /// Самая старая и самая новая сохранённая свеча (обе 1970-01-01, если свечей нет).
    async fn time_range(&self, symbol: &str, timeframe: Timeframe) -> Result<(u32, u32)> {
        let row: TimeRange = self
            .client
            .query(&format!(
                "SELECT min(timestamp), max(timestamp) FROM {} WHERE symbol = ? AND timeframe = ?",
                self.table
            ))
            .bind(symbol)
            .bind(timeframe.as_str())
            .fetch_one()
            .await
            .context("failed to query stored time range")?;
        Ok((row.min, row.max))
    }

    async fn save_candles(
        &self,
        symbol: &str,
        timeframe: Timeframe,
        candles: &[bybit::Candle],
    ) -> Result<()> {
        let mut insert = self
            .client
            .insert::<MarketDataRow>(&self.table)
            .context("prepare batch failed")?;
        for c in candles {
            insert
                .write(&MarketDataRow {
                    symbol: symbol.to_owned(),
                    timeframe,
                    timestamp: (c.timestamp_ms / 1000) as u32,
                    low: c.low,
                    open: c.open,
                    close: c.close,
                    high: c.high,
                    volume: c.volume,
                })
                .await
                .context("append to batch failed")?;
        }
        insert.end().await.context("send batch failed")?;
        Ok(())
    }

    /// Свечи за последние `months` месяцев по возрастанию времени.
    pub async fn load_market_data(
        &self,
        symbol: &str,
        timeframe: Timeframe,
        months: u32,
    ) -> Result<Quotes> {
        let period = months_ago(months);
        let rows: Vec<QuoteRow> = self
            .client
            .query(&format!(
                "SELECT timestamp, low, open, close, high, volume FROM {} \
                 WHERE symbol = ? AND timeframe = ? AND timestamp >= toDateTime(?) \
                 ORDER BY timestamp",
                self.table
            ))
            .bind(symbol)
            .bind(timeframe.as_str())
            .bind(period.timestamp().max(0) as u32)
            .fetch_all()
            .await
            .context("failed to query market data")?;

        let mut q = Quotes::default();
        for r in rows {
            q.timestamps.push(r.timestamp as i64);
            q.lows.push(r.low);
            q.opens.push(r.open);
            q.closes.push(r.close);
            q.highs.push(r.high);
            q.volumes.push(r.volume);
        }
        Ok(q)
    }
}

fn months_ago(months: u32) -> DateTime<Utc> {
    let now = Utc::now();
    now.checked_sub_months(Months::new(months)).unwrap_or(now)
}

fn fmt_ts(ms: i64) -> String {
    DateTime::from_timestamp_millis(ms)
        .map(|t| t.format("%Y-%m-%d %H:%M:%S").to_string())
        .unwrap_or_else(|| ms.to_string())
}

/// Докачивает в ClickHouse закрытые свечи за последние `months` месяцев, которых там ещё нет:
/// новые — после последней сохранённой, и старые — если сохранённая история короче периода.
pub async fn sync_market_data(
    db: &Db,
    symbol: &str,
    timeframe: Timeframe,
    months: u32,
) -> Result<()> {
    let (min_time, max_time) = db.time_range(symbol, timeframe).await?;
    let (min_ms, max_ms) = (min_time as i64 * 1000, max_time as i64 * 1000);
    let want_ms = months_ago(months).timestamp_millis();
    let now_ms = Utc::now().timestamp_millis();
    let tf_ms = timeframe.duration().as_millis() as i64;

    let mut ranges = Vec::new();
    if max_time < EMPTY_TABLE_THRESHOLD {
        info!(start_time = %fmt_ts(want_ms), months, "no market data found in clickhouse, syncing full history");
        ranges.push((want_ms, now_ms));
    } else {
        info!(min_time = %fmt_ts(min_ms), max_time = %fmt_ts(max_ms), "found existing market data in clickhouse");
        if min_ms > want_ms + tf_ms {
            info!(start_time = %fmt_ts(want_ms), months, "stored history is shorter than the period, fetching older candles");
            ranges.push((want_ms, min_ms - 1));
        }
        if now_ms > max_ms + 2 * tf_ms {
            ranges.push((max_ms, now_ms));
        }
    }
    if ranges.is_empty() {
        info!("market data is already up-to-date");
        return Ok(());
    }

    let http = reqwest::Client::builder()
        .timeout(Duration::from_secs(30))
        .build()?;
    let mut raw_candles = Vec::new();
    for (start_ms, end_ms) in ranges {
        raw_candles.extend(fetch_range(&http, symbol, timeframe, start_ms, end_ms).await?);
    }
    info!(
        raw_count = raw_candles.len(),
        "fetched all candles from bybit"
    );

    let mut to_insert = Vec::new();
    for raw in &raw_candles {
        let candle = match bybit::parse_candle(raw) {
            Ok(c) => c,
            Err(e) => {
                warn!(error = %e, "skipping invalid candle");
                continue;
            }
        };
        // Только полностью закрытые свечи вне уже сохранённого диапазона — без повторов.
        let complete = candle.timestamp_ms + tf_ms <= Utc::now().timestamp_millis();
        let unseen = candle.timestamp_ms > max_ms || candle.timestamp_ms < min_ms;
        if complete && unseen {
            to_insert.push(candle);
        }
    }
    info!(
        to_insert_count = to_insert.len(),
        "filtered candles for insertion"
    );

    if to_insert.is_empty() {
        info!("no new unique candles to insert");
        return Ok(());
    }

    db.save_candles(symbol, timeframe, &to_insert)
        .await
        .context("failed to save candles to clickhouse")?;
    info!(
        inserted_count = to_insert.len(),
        "successfully saved candles to clickhouse"
    );
    Ok(())
}

/// Все свечи Bybit в `[start_ms, end_ms]`: страницами по 1000 от новых к старым.
async fn fetch_range(
    http: &reqwest::Client,
    symbol: &str,
    timeframe: Timeframe,
    start_ms: i64,
    end_ms: i64,
) -> Result<Vec<Vec<String>>> {
    let mut current_end_ms = end_ms;
    let mut raw_candles = Vec::new();
    while current_end_ms > start_ms {
        info!(end_time = %fmt_ts(current_end_ms), "fetching batch from bybit kline API...");
        let batch = bybit::fetch_klines(http, symbol, timeframe, start_ms, current_end_ms)
            .await
            .context("failed to fetch klines from bybit")?;
        let Some(oldest) = batch.last() else {
            break;
        };
        // Bybit отдаёт новые первыми, самая старая свеча — последняя.
        let oldest_ms: i64 = oldest
            .first()
            .context("empty candle row")?
            .parse()
            .context("failed to parse oldest candle timestamp")?;
        raw_candles.extend(batch);

        if oldest_ms <= start_ms {
            break;
        }
        current_end_ms = oldest_ms - 1;
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    Ok(raw_candles)
}
