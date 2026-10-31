use std::path::Path;
use std::process::ExitCode;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use anyhow::{bail, Context, Result};
use tokio_util::sync::CancellationToken;
use tracing::{error, info};

use crypto_back::analyzer::Analyzer;
use crypto_back::config::{self, Config};
use crypto_back::fan::FanBoost;
use crypto_back::storage::{self, Db};
use crypto_back::timeframe::Timeframe;
use crypto_back::{logger, server};

fn main() -> ExitCode {
    let result = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(anyhow::Error::from)
        .and_then(|rt| rt.block_on(run()));

    match result {
        Ok(()) => {
            println!("application is stopped");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("application stopped with error: {e:#}");
            ExitCode::FAILURE
        }
    }
}

async fn run() -> Result<()> {
    let dir = config::config_dir()?;
    let cfg = Config::load_from(&dir)?;
    let _log_guard = logger::init(&cfg.log)?;

    let shutdown = CancellationToken::new();
    let stop = Arc::new(AtomicBool::new(false));
    tokio::spawn(wait_kill_signal(shutdown.clone(), stop.clone()));

    let db = Db::connect(&cfg.db)
        .await
        .context("clickhouse initialization error")?;
    info!("application started");

    if cfg.all_pairs() {
        return search_all_pairs(&dir, &cfg, &db, &shutdown, &stop).await;
    }

    let Some(az) = load_analyzer(&cfg, &db, &shutdown).await? else {
        return Ok(());
    };

    let server = cfg
        .http
        .active
        .then(|| tokio::spawn(server::serve(az.clone(), cfg.http.port, shutdown.clone())));

    if cfg.analyzer.active {
        let _fans = fans(cfg.analyzer.fan_speed);
        search(&cfg, az, &stop).await?;
    } else {
        println!("strategy search skipped: analyzer.active is false");
        info!("strategy search skipped: analyzer.active is false");
    }

    info!("waiting for background tasks or shutdown signal...");
    if let Some(server) = server {
        if let Err(e) = server.await? {
            error!(error = %format!("{e:#}"), "background task failed");
        }
    }
    Ok(())
}

/// Все пары `<direction>/*.yaml` по очереди, без HTTP API. Ошибка одной пары не останавливает
/// остальные; в конце — сводка лучших результатов.
async fn search_all_pairs(
    dir: &Path,
    cfg: &Config,
    db: &Db,
    shutdown: &CancellationToken,
    stop: &Arc<AtomicBool>,
) -> Result<()> {
    if !cfg.analyzer.active {
        bail!("analyzer.pair is not set: searching all pairs needs analyzer.active");
    }
    if cfg.http.active {
        println!("HTTP API is not started when searching all pairs");
    }
    let _fans = fans(cfg.analyzer.fan_speed);

    let mut summary = Vec::new();
    for pair in config::pair_names(dir, cfg.analyzer.direction)? {
        println!("\n=== {pair}");
        let result = async {
            let cfg = Config::load_pair(dir, Some(&pair))?;
            match load_analyzer(&cfg, db, shutdown).await? {
                Some(az) => search(&cfg, az, stop).await.map(Some),
                None => Ok(None),
            }
        }
        .await;
        if stop.load(Ordering::Relaxed) {
            break;
        }
        match result {
            Ok(Some(line)) => summary.push(format!("{pair:<10} {line}")),
            Ok(None) => break,
            Err(e) => {
                println!("{pair}: {e:#}");
                error!(pair, error = %format!("{e:#}"), "pair search failed");
                summary.push(format!("{pair:<10} error: {e:#}"));
            }
        }
    }
    println!("\n{}", summary.join("\n"));
    Ok(())
}

/// Свечи пары из Bybit в ClickHouse и анализатор над ними. `None` — остановлено сигналом.
async fn load_analyzer(
    cfg: &Config,
    db: &Db,
    shutdown: &CancellationToken,
) -> Result<Option<Arc<Analyzer>>> {
    let pair = cfg.analyzer.pair.as_str();
    let timeframe: Timeframe = cfg.analyzer.timeframe.parse()?;
    let months = cfg.analyzer.history_months();

    tokio::select! {
        res = storage::sync_market_data(db, pair, timeframe, months) => res.context("market data sync failed")?,
        _ = shutdown.cancelled() => return Ok(None),
    }

    let quotes = db
        .load_market_data(pair, timeframe, months)
        .await
        .context("failed to load market data")?;
    info!(candles = quotes.len(), "market data loaded");
    Ok(Some(Arc::new(Analyzer::new(cfg.analyzer.clone(), quotes))))
}

/// Перебор стратегий. Если его не прервали, лучший результат записывается первой строкой
/// конфига пары.
async fn search(cfg: &Config, az: Arc<Analyzer>, stop: &Arc<AtomicBool>) -> Result<String> {
    let start = Instant::now();
    let stop_run = stop.clone();
    let best = tokio::task::spawn_blocking(move || az.run(&stop_run)).await??;
    println!("Execution time: {:?}", start.elapsed());

    let line = best.map_or_else(
        || "nothing found: no strategy passed the filters".to_string(),
        |b| b.plain(),
    );
    if let (Some(file), false) = (&cfg.pair_file, stop.load(Ordering::Relaxed)) {
        config::write_best(file, &line)?;
    }
    Ok(line)
}

/// Держит заданные обороты, пока жив; `fan auto` — при его удалении.
fn fans(rpm: u32) -> Option<FanBoost> {
    (rpm > 0)
        .then(|| {
            FanBoost::start(rpm)
                .inspect_err(|e| {
                    println!("fan control unavailable: {e:#}");
                    error!(error = %format!("{e:#}"), "fan control unavailable");
                })
                .ok()
        })
        .flatten()
}

/// SIGINT/SIGTERM останавливают HTTP-сервер и перебор (Go-версия перебор не прерывала).
async fn wait_kill_signal(shutdown: CancellationToken, stop: Arc<AtomicBool>) {
    let ctrl_c = tokio::signal::ctrl_c();
    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut s) => {
                s.recv().await;
            }
            Err(_) => std::future::pending::<()>().await,
        }
    };
    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }
    info!("shutdown signal received");
    stop.store(true, Ordering::Relaxed);
    shutdown.cancel();
}
