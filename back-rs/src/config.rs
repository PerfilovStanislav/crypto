//! Конфиг читается так же, как в Go-версии (cleanenv):
//! значения по умолчанию → `config.yaml` → `config_{PAIR}.yaml` → переменные окружения.
//! Каталог с конфигами задаётся `CONFIG_DIR` (по умолчанию текущий).

use std::env;
use std::path::{Path, PathBuf};
use std::str::FromStr;

use anyhow::{bail, Context, Result};
use figment::providers::{Format, Yaml};
use figment::Figment;
use serde::Deserialize;

use crate::indicator::IndicatorType;
use crate::source::SourceType;

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Config {
    pub log: LogConfig,
    pub db: DbConfig,
    pub http: HttpConfig,
    pub analyzer: AnalyzerConfig,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(default)]
pub struct LogConfig {
    pub level: String,
    pub path: String,
    pub days: usize,
    pub size: usize,
}

impl Default for LogConfig {
    fn default() -> Self {
        Self {
            level: "info".into(),
            path: "./logs/cnd.log".into(),
            days: 1,
            size: 100,
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
#[serde(default)]
pub struct DbConfig {
    pub host: String,
    pub port: u16,
    pub user: String,
    pub password: String,
    pub database: String,
}

impl Default for DbConfig {
    fn default() -> Self {
        Self {
            host: "localhost".into(),
            port: 5432,
            user: "appuser".into(),
            password: "qwerty12".into(),
            database: "mobstra".into(),
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
#[serde(default)]
pub struct HttpConfig {
    pub port: u16,
    pub active: bool,
}

impl Default for HttpConfig {
    fn default() -> Self {
        Self {
            port: 8888,
            active: false,
        }
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct AnalyzerConfig {
    pub threads: usize,
    pub pair: String,
    pub timeframe: String,
    pub commission: f64,
    pub decimals: i32,
    pub min_closes: usize,
    pub min_signals: usize,
    pub min_signals_last_months: usize,
    pub last_months_limit: u32,
    pub min_take_profits: usize,
    pub takeprofit: RangeConfig,
    pub stoploss: RangeConfig,
    pub indicators: Vec<Vec<IndicatorConfig>>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct IndicatorConfig {
    #[serde(rename = "type")]
    pub kind: IndicatorType,
    #[serde(default)]
    pub coefs: RangeConfig,
    #[serde(default)]
    pub sources: Vec<SourceType>,
}

#[derive(Debug, Clone, Copy, Default, Deserialize)]
#[serde(default)]
pub struct RangeConfig {
    pub start: f64,
    pub end: f64,
    pub step: f64,
}

impl RangeConfig {
    fn scale_down(&mut self, factor: f64) {
        self.start /= factor;
        self.end /= factor;
        self.step /= factor;
    }
}

impl Config {
    pub fn load() -> Result<Self> {
        let dir = env::var_os("CONFIG_DIR")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("."));
        Self::load_from(&dir)
    }

    pub fn load_from(dir: &Path) -> Result<Self> {
        let base = existing(dir.join("config.yaml"))?;
        let mut cfg: Config = Figment::new()
            .merge(Yaml::file(&base))
            .extract()
            .with_context(|| format!("failed to parse {}", base.display()))?;
        apply_env(&mut cfg)?;

        if !cfg.analyzer.pair.is_empty() {
            let pair = existing(dir.join(format!("config_{}.yaml", cfg.analyzer.pair)))?;
            cfg = Figment::new()
                .merge(Yaml::file(&base))
                .merge(Yaml::file(&pair))
                .extract()
                .with_context(|| format!("failed to parse {}", pair.display()))?;
            apply_env(&mut cfg)?;
        }

        if cfg.analyzer.decimals > 0 {
            let factor = 10f64.powi(cfg.analyzer.decimals);
            cfg.analyzer.takeprofit.scale_down(factor);
            cfg.analyzer.stoploss.scale_down(factor);
        }

        Ok(cfg)
    }
}

fn existing(path: PathBuf) -> Result<PathBuf> {
    if !path.is_file() {
        bail!("config file {} not found", path.display());
    }
    Ok(path)
}

/// Те же имена переменных, что давали теги `env` + `env-prefix` в Go.
fn apply_env(cfg: &mut Config) -> Result<()> {
    env_parse("LOG_LEVEL", &mut cfg.log.level)?;
    env_parse("LOG_PATH", &mut cfg.log.path)?;

    env_parse("DB_HOST", &mut cfg.db.host)?;
    env_parse("DB_PORT", &mut cfg.db.port)?;
    env_parse("DB_USER", &mut cfg.db.user)?;
    env_parse("DB_PASSWORD", &mut cfg.db.password)?;
    env_parse("DB_DATABASE", &mut cfg.db.database)?;

    env_parse("HTTP_PORT", &mut cfg.http.port)?;
    if let Some(v) = env_value("HTTP_ACTIVE") {
        cfg.http.active = parse_bool(&v).context("HTTP_ACTIVE")?;
    }

    let a = &mut cfg.analyzer;
    env_parse("ANALYZER_THREADS", &mut a.threads)?;
    env_parse("ANALYZER_PAIR", &mut a.pair)?;
    env_parse("ANALYZER_TIMEFRAME", &mut a.timeframe)?;
    env_parse("ANALYZER_COMMISSION", &mut a.commission)?;
    env_parse("ANALYZER_DECIMALS", &mut a.decimals)?;
    env_parse("ANALYZER_MIN_CLOSES", &mut a.min_closes)?;
    env_parse("ANALYZER_MIN_SIGNALS", &mut a.min_signals)?;
    env_parse(
        "ANALYZER_MIN_SIGNALS_LAST_MONTHS",
        &mut a.min_signals_last_months,
    )?;
    env_parse("ANALYZER_LAST_MONTHS_LIMIT", &mut a.last_months_limit)?;
    env_parse("ANALYZER_MIN_TAKE_PROFITS", &mut a.min_take_profits)?;
    Ok(())
}

fn env_value(key: &str) -> Option<String> {
    env::var(key).ok().filter(|v| !v.is_empty())
}

fn env_parse<T>(key: &str, target: &mut T) -> Result<()>
where
    T: FromStr,
    T::Err: std::fmt::Display,
{
    if let Some(v) = env_value(key) {
        *target = v
            .parse()
            .map_err(|e| anyhow::anyhow!("invalid {key}={v:?}: {e}"))?;
    }
    Ok(())
}

/// Как `strconv.ParseBool` в Go.
fn parse_bool(v: &str) -> Result<bool> {
    match v {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => bail!("invalid bool {v:?}"),
    }
}
