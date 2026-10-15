use std::path::Path;

use anyhow::{Context, Result};
use tracing_appender::non_blocking::WorkerGuard;
use tracing_appender::rolling::{RollingFileAppender, Rotation};
use tracing_subscriber::filter::LevelFilter;

use crate::config::LogConfig;

/// JSON-логи в файл с суточной ротацией; хранится `days` последних файлов.
/// Ротации по размеру (`size`) в tracing-appender нет, параметр игнорируется.
/// Возвращённый guard нужно держать до конца `main`, иначе хвост логов потеряется.
pub fn init(cfg: &LogConfig) -> Result<WorkerGuard> {
    let path = Path::new(&cfg.path);
    let dir = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let file_name = path
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("crypto.log");

    std::fs::create_dir_all(dir)
        .with_context(|| format!("failed to create log dir {}", dir.display()))?;
    let appender = RollingFileAppender::builder()
        .rotation(Rotation::DAILY)
        .filename_prefix(file_name)
        .max_log_files(cfg.days.max(1))
        .build(dir)
        .with_context(|| format!("failed to open log file in {}", dir.display()))?;
    let (writer, guard) = tracing_appender::non_blocking(appender);

    let level = cfg
        .level
        .parse::<LevelFilter>()
        .unwrap_or(LevelFilter::INFO);

    tracing_subscriber::fmt()
        .json()
        .with_max_level(level)
        .with_writer(writer)
        .with_current_span(true)
        .try_init()
        .map_err(|e| anyhow::anyhow!("failed to init logger: {e}"))?;

    Ok(guard)
}
