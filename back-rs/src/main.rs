use std::process::ExitCode;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use anyhow::{Context, Result};
use tokio_util::sync::CancellationToken;
use tracing::{error, info};

use crypto_back::analyzer::Analyzer;
use crypto_back::config::Config;
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
    let cfg = Config::load()?;
    let _log_guard = logger::init(&cfg.log)?;

    let shutdown = CancellationToken::new();
    let stop = Arc::new(AtomicBool::new(false));
    tokio::spawn(wait_kill_signal(shutdown.clone(), stop.clone()));

    let db = Db::connect(&cfg.db)
        .await
        .context("clickhouse initialization error")?;
    info!("application started");

    let pair = cfg.analyzer.pair.as_str();
    let timeframe: Timeframe = cfg.analyzer.timeframe.parse()?;

    tokio::select! {
        res = storage::sync_market_data(&db, pair, timeframe) => res.context("market data sync failed")?,
        _ = shutdown.cancelled() => return Ok(()),
    }

    let quotes = db
        .load_market_data(pair, timeframe)
        .await
        .context("failed to load market data")?;
    info!(candles = quotes.len(), "market data loaded");

    let az = Arc::new(Analyzer::new(cfg.analyzer.clone(), quotes));

    let server = cfg
        .http
        .active
        .then(|| tokio::spawn(server::serve(az.clone(), cfg.http.port, shutdown.clone())));

    if cfg.analyzer.active {
        // Держит заданные обороты, пока жив; `fan auto` — при выходе из этого блока.
        let rpm = cfg.analyzer.fan_speed;
        let _fans = (rpm > 0).then(|| {
            FanBoost::start(rpm)
                .inspect_err(|e| {
                    println!("fan control unavailable: {e:#}");
                    error!(error = %format!("{e:#}"), "fan control unavailable");
                })
                .ok()
        });
        let start = Instant::now();
        let (az_run, stop_run) = (az.clone(), stop.clone());
        tokio::task::spawn_blocking(move || az_run.run(&stop_run)).await??;
        println!("Execution time: {:?}", start.elapsed());
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
