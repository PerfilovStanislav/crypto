use std::str::FromStr;
use std::time::Duration;

use anyhow::bail;
use serde_repr::{Deserialize_repr, Serialize_repr};

/// Таймфрейм свечей. Числовые значения совпадают с Enum8 `market_data.timeframe`
/// из `core/init-scripts/init.sql`, поэтому `1w` (нет в Enum8) не поддерживается.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize_repr, Deserialize_repr)]
#[repr(i8)]
pub enum Timeframe {
    M1 = 1,
    M3 = 2,
    M5 = 3,
    M15 = 4,
    M30 = 5,
    H1 = 6,
    H2 = 7,
    H4 = 8,
    D1 = 9,
    // Добавлены позже: номера в Enum8 после 1d, чтобы не перенумеровать сохранённые свечи.
    H6 = 10,
    H12 = 11,
}

impl Timeframe {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::M1 => "1m",
            Self::M3 => "3m",
            Self::M5 => "5m",
            Self::M15 => "15m",
            Self::M30 => "30m",
            Self::H1 => "1h",
            Self::H2 => "2h",
            Self::H4 => "4h",
            Self::H6 => "6h",
            Self::H12 => "12h",
            Self::D1 => "1d",
        }
    }

    pub fn duration(self) -> Duration {
        let minutes = match self {
            Self::M1 => 1,
            Self::M3 => 3,
            Self::M5 => 5,
            Self::M15 => 15,
            Self::M30 => 30,
            Self::H1 => 60,
            Self::H2 => 120,
            Self::H4 => 240,
            Self::H6 => 360,
            Self::H12 => 720,
            Self::D1 => 1440,
        };
        Duration::from_secs(minutes * 60)
    }

    /// Значение параметра `interval` в Bybit v5 kline API.
    pub fn bybit_interval(self) -> &'static str {
        match self {
            Self::M1 => "1",
            Self::M3 => "3",
            Self::M5 => "5",
            Self::M15 => "15",
            Self::M30 => "30",
            Self::H1 => "60",
            Self::H2 => "120",
            Self::H4 => "240",
            Self::H6 => "360",
            Self::H12 => "720",
            Self::D1 => "D",
        }
    }
}

impl FromStr for Timeframe {
    type Err = anyhow::Error;

    fn from_str(s: &str) -> anyhow::Result<Self> {
        Ok(match s {
            "1m" => Self::M1,
            "3m" => Self::M3,
            "5m" => Self::M5,
            "15m" => Self::M15,
            "30m" => Self::M30,
            "1h" => Self::H1,
            "2h" => Self::H2,
            "4h" => Self::H4,
            "6h" => Self::H6,
            "12h" => Self::H12,
            "1d" => Self::D1,
            _ => bail!("unsupported timeframe {s:?}"),
        })
    }
}
