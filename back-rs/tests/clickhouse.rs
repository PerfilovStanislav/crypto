//! Синк свечей с Bybit во временную таблицу и сверка с тем, что записала Go-версия.
//! Нужен ClickHouse из `core/docker-compose.yml` и доступ к api.bybit.com:
//!   cargo test --release --test clickhouse -- --ignored --nocapture

use std::collections::HashMap;

use crypto_back::config::Config;
use crypto_back::storage::{self, Db, MARKET_DATA_TABLE};
use crypto_back::timeframe::Timeframe;

const TEST_TABLE: &str = "default.market_data_rs_test";

#[tokio::test]
#[ignore = "needs ClickHouse and network access to Bybit"]
async fn sync_writes_same_candles_as_go() {
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
        storage::sync_market_data(&test, pair, tf).await?;
        let first = test.load_market_data(pair, tf).await?;
        // Повторный синк ничего не должен дописать.
        storage::sync_market_data(&test, pair, tf).await?;
        let second = test.load_market_data(pair, tf).await?;
        let go = main.load_market_data(pair, tf).await?;
        anyhow::Ok((first, second, go))
    }
    .await;
    ddl(format!("DROP TABLE IF EXISTS {TEST_TABLE}"))
        .await
        .unwrap();
    let (ours, again, go) = result.unwrap();

    eprintln!(
        "rust rows: {}, after re-sync: {}, go rows: {}",
        ours.len(),
        again.len(),
        go.len()
    );
    assert!(ours.len() > 4000, "2 years of 4h candles");
    assert_eq!(ours.len(), again.len(), "re-sync is a no-op");
    assert!(
        ours.timestamps.windows(2).all(|w| w[0] < w[1]),
        "sorted and without duplicates"
    );

    let go_by_ts: HashMap<i64, usize> = go
        .timestamps
        .iter()
        .enumerate()
        .map(|(i, &t)| (t, i))
        .collect();
    let mut compared = 0;
    for (i, ts) in ours.timestamps.iter().enumerate() {
        let Some(&j) = go_by_ts.get(ts) else { continue };
        let row = |q: &crypto_back::source::Quotes, k: usize| {
            (q.opens[k], q.highs[k], q.lows[k], q.closes[k], q.volumes[k])
        };
        assert_eq!(row(&ours, i), row(&go, j), "candle at {ts}");
        compared += 1;
    }
    eprintln!("identical candles: {compared}");
    assert!(compared > 4000);
}
