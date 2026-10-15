use std::fmt;

use serde::{Deserialize, Serialize};

/// Ценовой источник для индикатора: одна из колонок свечи или среднее нескольких.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum SourceType {
    L,
    O,
    C,
    H,
    V,
    LO,
    LC,
    LH,
    OC,
    OH,
    CH,
    LOC,
    LOH,
    LCH,
    OCH,
}

impl SourceType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::L => "L",
            Self::O => "O",
            Self::C => "C",
            Self::H => "H",
            Self::V => "V",
            Self::LO => "LO",
            Self::LC => "LC",
            Self::LH => "LH",
            Self::OC => "OC",
            Self::OH => "OH",
            Self::CH => "CH",
            Self::LOC => "LOC",
            Self::LOH => "LOH",
            Self::LCH => "LCH",
            Self::OCH => "OCH",
        }
    }
}

impl fmt::Display for SourceType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.pad(self.as_str())
    }
}

/// Котировки в виде колонок (SoA). `timestamps` — unix-секунды начала свечи.
#[derive(Debug, Clone, Default)]
pub struct Quotes {
    pub timestamps: Vec<i64>,
    pub lows: Vec<f64>,
    pub opens: Vec<f64>,
    pub closes: Vec<f64>,
    pub highs: Vec<f64>,
    pub volumes: Vec<f64>,
}

impl Quotes {
    pub fn len(&self) -> usize {
        self.opens.len()
    }

    pub fn is_empty(&self) -> bool {
        self.opens.is_empty()
    }

    pub fn prices(&self, source: SourceType) -> Vec<f64> {
        use SourceType::*;
        match source {
            L => self.lows.clone(),
            O => self.opens.clone(),
            C => self.closes.clone(),
            H => self.highs.clone(),
            V => self.volumes.clone(),
            LO => average_two(&self.lows, &self.opens),
            LC => average_two(&self.lows, &self.closes),
            LH => average_two(&self.lows, &self.highs),
            OC => average_two(&self.opens, &self.closes),
            OH => average_two(&self.opens, &self.highs),
            CH => average_two(&self.closes, &self.highs),
            LOC => average_three(&self.lows, &self.opens, &self.closes),
            LOH => average_three(&self.lows, &self.opens, &self.highs),
            LCH => average_three(&self.lows, &self.closes, &self.highs),
            OCH => average_three(&self.opens, &self.closes, &self.highs),
        }
    }
}

pub fn average_two(s1: &[f64], s2: &[f64]) -> Vec<f64> {
    s1.iter().zip(s2).map(|(a, b)| (a + b) / 2.0).collect()
}

pub fn average_three(s1: &[f64], s2: &[f64], s3: &[f64]) -> Vec<f64> {
    s1.iter()
        .zip(s2)
        .zip(s3)
        .map(|((a, b), c)| (a + b + c) / 3.0)
        .collect()
}
