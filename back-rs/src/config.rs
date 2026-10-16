//! Конфиг читается так же, как в Go-версии (cleanenv):
//! значения по умолчанию → `config.yaml` → `config_{PAIR}.yaml` → переменные окружения.
//! Каталог с конфигами — `CONFIG_DIR`, а если он не задан, ближайший `configs/` с `config.yaml`
//! вверх от текущего (в репозитории это `configs/` рядом с `back/` и `back-rs/`).

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
    /// Запускать перебор стратегий. Без него работают только синк свечей и HTTP API.
    pub active: bool,
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
    /// Быстрый проход перебора на GPU (нужна сборка с `--features gpu`).
    pub gpu: bool,
    /// Обороты вентиляторов (об/мин) на время перебора: `sudo fan set N`, потом `fan auto`
    /// (см. `fan.rs`). 0 — вентиляторы не трогать.
    pub fan_speed: u32,
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
        Self::load_from(&config_dir()?)
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

/// `CONFIG_DIR` или ближайший каталог `configs/` с `config.yaml` вверх от текущего.
pub fn config_dir() -> Result<PathBuf> {
    if let Some(dir) = env::var_os("CONFIG_DIR").filter(|d| !d.is_empty()) {
        return Ok(dir.into());
    }
    let cwd = env::current_dir()?;
    find_configs(&cwd).with_context(|| {
        format!(
            "configs/config.yaml not found in {} or its parents; set CONFIG_DIR",
            cwd.display()
        )
    })
}

fn find_configs(start: &Path) -> Option<PathBuf> {
    start
        .ancestors()
        .map(|d| d.join("configs"))
        .find(|d| d.join("config.yaml").is_file())
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
    if let Some(v) = env_value("ANALYZER_GPU") {
        a.gpu = parse_bool(&v).context("ANALYZER_GPU")?;
    }
    env_parse("ANALYZER_FAN_SPEED", &mut a.fan_speed)?;
    if let Some(v) = env_value("ANALYZER_ACTIVE") {
        a.active = parse_bool(&v).context("ANALYZER_ACTIVE")?;
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn finds_configs_in_parent_dirs() {
        let root = env::temp_dir().join(format!(
            "configs-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let nested = root.join("back").join("src");
        std::fs::create_dir_all(&nested).unwrap();
        std::fs::create_dir_all(root.join("configs")).unwrap();
        assert_eq!(find_configs(&nested), None, "no config.yaml yet");

        std::fs::write(root.join("configs").join("config.yaml"), "").unwrap();
        assert_eq!(find_configs(&nested), Some(root.join("configs")));
        assert_eq!(find_configs(&root), Some(root.join("configs")));

        std::fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn analyzer_is_inactive_unless_enabled() {
        let dir = env::temp_dir().join(format!("active-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let load = |yaml: &str| {
            std::fs::write(dir.join("config.yaml"), yaml).unwrap();
            Config::load_from(&dir).unwrap().analyzer.active
        };
        assert!(!load("analyzer:\n  threads: 1\n"), "off by default");
        assert!(load("analyzer:\n  active: true\n"));
        assert!(!load("analyzer:\n  active: false\n"));
        std::fs::remove_dir_all(&dir).unwrap();
    }

    /// Конфиги репозитория читаются из `configs/`. `config.yaml` не в git — без него тест пропускается.
    #[test]
    fn loads_repo_configs() {
        let Some(dir) = find_configs(Path::new(env!("CARGO_MANIFEST_DIR"))) else {
            eprintln!("skip: configs/config.yaml not found");
            return;
        };
        let cfg = Config::load_from(&dir).unwrap();
        assert!(!cfg.analyzer.pair.is_empty());
        assert!(!cfg.analyzer.indicators.is_empty(), "pair config is merged");
    }
}
