use std::fmt;

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum IndicatorType {
    Sma,
    Ema,
    Dema,
    Tema,
    TemaZero,
}

impl IndicatorType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Sma => "sma",
            Self::Ema => "ema",
            Self::Dema => "dema",
            Self::Tema => "tema",
            Self::TemaZero => "temaZero",
        }
    }

    /// Для SMA `coef` — длина окна (дробная часть отбрасывается), для остальных — множитель EMA.
    pub fn calculate(self, prices: &[f64], coef: f64) -> Vec<f64> {
        match self {
            Self::Sma => sma(prices, coef as i64),
            Self::Ema => ema(prices, coef),
            Self::Dema => dema(prices, coef),
            Self::Tema => tema(prices, coef),
            Self::TemaZero => tema_zero(prices, coef),
        }
    }
}

impl fmt::Display for IndicatorType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.pad(self.as_str())
    }
}

/// Скользящее среднее. Первые `n` значений — среднее по доступному префиксу.
/// Go-версия паниковала при `n < 1` и `n > len`; здесь окно ограничено `1..=len`.
fn sma(prices: &[f64], n: i64) -> Vec<f64> {
    let len = prices.len();
    if len == 0 {
        return Vec::new();
    }
    let n = n.clamp(1, len as i64) as usize;
    let mut result = vec![0.0; len];
    result[0] = prices[0];
    for i in 1..n {
        result[i] = (result[i - 1] * i as f64 + prices[i]) / (i + 1) as f64;
    }
    for i in n..len {
        result[i] = result[i - 1] + (prices[i] - prices[i - n]) / n as f64;
    }
    result
}

fn ema(prices: &[f64], multiplier: f64) -> Vec<f64> {
    let Some(&first) = prices.first() else {
        return Vec::new();
    };
    let mut result = Vec::with_capacity(prices.len());
    result.push(first);
    let mut prev = first;
    for &price in &prices[1..] {
        prev = (price * multiplier) + (prev * (1.0 - multiplier));
        result.push(prev);
    }
    result
}

fn dema(prices: &[f64], multiplier: f64) -> Vec<f64> {
    let ema1 = ema(prices, multiplier);
    let ema2 = ema(&ema1, multiplier);
    ema1.iter()
        .zip(&ema2)
        .map(|(e1, e2)| (2.0 * e1) - e2)
        .collect()
}

fn tema(prices: &[f64], multiplier: f64) -> Vec<f64> {
    let ema1 = ema(prices, multiplier);
    let ema2 = ema(&ema1, multiplier);
    let ema3 = ema(&ema2, multiplier);
    ema1.iter()
        .zip(&ema2)
        .zip(&ema3)
        .map(|((e1, e2), e3)| (3.0 * e1) - (3.0 * e2) + e3)
        .collect()
}

fn tema_zero(prices: &[f64], multiplier: f64) -> Vec<f64> {
    let tema1 = tema(prices, multiplier);
    let tema2 = tema(&tema1, multiplier);
    tema1
        .iter()
        .zip(&tema2)
        .map(|(t1, t2)| (2.0 * t1) - t2)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sma_matches_naive_window_average() {
        let prices = [1.0, 2.0, 3.0, 4.0, 5.0, 6.0];
        let got = sma(&prices, 3);
        let want = [1.0, 1.5, 2.0, 3.0, 4.0, 5.0];
        for (g, w) in got.iter().zip(want) {
            assert!((g - w).abs() < 1e-12, "{got:?}");
        }
    }

    #[test]
    fn sma_tolerates_degenerate_window() {
        assert_eq!(sma(&[], 3), Vec::<f64>::new());
        assert_eq!(sma(&[1.0, 2.0], 0).len(), 2);
        assert_eq!(sma(&[1.0, 2.0], 10).len(), 2);
    }

    #[test]
    fn ema_with_multiplier_one_is_identity() {
        let prices = [3.0, 1.0, 4.0, 1.0, 5.0];
        assert_eq!(ema(&prices, 1.0), prices);
    }
}
