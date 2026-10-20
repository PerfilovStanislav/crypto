//! Синк свечей с Bybit во временную таблицу: загрузка периода, догрузка старой истории при
//! увеличении периода, повторный синк без дублей и сверка с основной таблицей.
//! Нужен ClickHouse из `core/docker-compose.yml` и доступ к api.bybit.com:
//!   cargo test --release --test clickhouse -- --ignored --nocapture

use std::collections::HashMap;

use crypto_back::config::Config;
use crypto_back::storage::{self, Db, MARKET_DATA_TABLE};
use crypto_back::timeframe::Timeframe;

const TEST_TABLE: &str = "default.market_data_rs_test";

#[tokio::test]
#[ignore = "needs ClickHouse and network access to Bybit"]
async fn sync_backfills_history_and_matches_main_table() {
    let cfg = Config::load().unwrap();
    let tf: Timeframe = cfg.analyzer.timeframe.parse().unwrap();
    let pair = cfg.analyzer.pair.as_str();

    let main = Db::connect(&cfg.db).await.unwrap();
    let main = &main;
    let ddl = |sql: String| async move { main.client().query(&sql).execute().await };
    ddl(format!("DROP TABLE IF EXISTS {TEST_TABLE}"))
        .await
        .unwrap();
    ddl(format!("CREATE TABLE {TEST_TABLE} AS {MARKET_DATA_TABLE}"))
        .await
        .unwrap();

    let test = Db::connect(&cfg.db).await.unwrap().with_table(TEST_TABLE);
    let result = async {
        storage::sync_market_data(&test, pair, tf, 6).await?;
        let short = test.load_market_data(pair, tf, 24).await?;
        // Период вырос: синк должен догрузить старые свечи, а не только новые.
        storage::sync_market_data(&test, pair, tf, 12).await?;
        let full = test.load_market_data(pair, tf, 24).await?;
        // Повторный синк ничего не должен дописать.
        storage::sync_market_data(&test, pair, tf, 12).await?;
        let again = test.load_market_data(pair, tf, 24).await?;
        let reference = main.load_market_data(pair, tf, 12).await?;
        anyhow::Ok((short, full, again, reference))
    }
    .await;
    ddl(format!("DROP TABLE IF EXISTS {TEST_TABLE}"))
        .await
        .unwrap();
    let (short, full, again, reference) = result.unwrap();

    eprintln!(
        "6 months: {} rows, 12 months: {}, after re-sync: {}, main table: {}",
        short.len(),
        full.len(),
        again.len(),
        reference.len()
    );
    assert!(!short.is_empty());
    assert!(
        full.len() * 2 > short.len() * 3,
        "12 months must be roughly twice 6 months"
    );
    assert!(
        full.timestamps[0] < short.timestamps[0],
        "older candles were backfilled"
    );
    assert_eq!(again.len(), full.len(), "re-sync is a no-op");
    assert!(
        full.timestamps.windows(2).all(|w| w[0] < w[1]),
        "sorted and without duplicates"
    );

    let by_ts: HashMap<i64, usize> = reference
        .timestamps
        .iter()
        .enumerate()
        .map(|(i, &t)| (t, i))
        .collect();
    let mut compared = 0;
    for (i, ts) in full.timestamps.iter().enumerate() {
        let Some(&j) = by_ts.get(ts) else { continue };
        let row = |q: &crypto_back::source::Quotes, k: usize| {
            (q.opens[k], q.highs[k], q.lows[k], q.closes[k], q.volumes[k])
        };
        assert_eq!(row(&full, i), row(&reference, j), "candle at {ts}");
        compared += 1;
    }
    eprintln!("identical candles with the main table: {compared}");
}
