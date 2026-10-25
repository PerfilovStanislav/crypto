//! Мартингейл: первая сделка серии идёт на долю текущего капитала (базу). После SL ставка растёт,
//! чтобы TP принёс обычную прибыль базы и вернул убыток серии × `coef`; после TP ставка снова
//! базовая. Серия обрывается, если ставки не хватает капитала или SL подряд стало `max_losses`.

use super::fast::{finish, next_signal, Tally};
use super::{Filters, Metrics, Outcomes};
use crate::config::AnalyzerConfig;

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Martingale {
    /// Во сколько раз TP после серии SL перекрывает её убыток.
    pub coef: f64,
    /// Доля капитала на первую сделку серии.
    pub base: f64,
    /// Сколько SL подряд обрывают серию; 0 — без ограничения.
    pub max_losses: usize,
}

impl Martingale {
    /// `None` — мартингейл в конфиге выключен.
    pub fn from_config(cfg: &AnalyzerConfig) -> Option<Self> {
        (cfg.martingale > 0.0).then(|| Self {
            coef: cfg.martingale,
            base: cfg.deposit_percent / 100.0,
            max_losses: cfg.max_losses,
        })
    }
}

/// Капитал по ходу сделок, в долях начального.
#[derive(Debug, Clone)]
pub struct Series {
    m: Martingale,
    cms: f64,
    pub capital: f64,
    peak: f64,
    base_stake: f64,
    /// Убыток текущей серии SL.
    loss: f64,
    losses_in_row: usize,
    /// Наибольшая просадка капитала от пика, доля.
    pub max_drawdown: f64,
}

impl Series {
    pub fn new(m: Martingale, commission: f64) -> Self {
        Self {
            m,
            cms: (1.0 - commission) * (1.0 - commission),
            capital: 1.0,
            peak: 1.0,
            base_stake: 0.0,
            loss: 0.0,
            losses_in_row: 0,
            max_drawdown: 0.0,
        }
    }

    /// Ставка на сделку со входом `entry` и тейк-профитом `tp` (в единицах цены), как в
    /// `calculate_closings`. `None` — ставка больше капитала или TP не окупает комиссию.
    pub fn stake(&mut self, entry: f64, tp: f64) -> Option<f64> {
        let stake = if self.losses_in_row == 0 {
            self.base_stake = self.m.base * self.capital;
            self.base_stake
        } else {
            // Множитель ставки на TP одинаков для лонга и шорта.
            let tp_gain = self.cms * (entry + tp) / entry - 1.0;
            self.base_stake + self.m.coef * self.loss / tp_gain
        };
        (stake > 0.0 && stake <= self.capital).then_some(stake)
    }

    /// Закрывает сделку: `coef` — множитель ставки, `drawdown` — худшая цена внутри сделки как
    /// доля входа. Возвращает множитель капитала.
    pub fn settle(&mut self, stake: f64, coef: f64, drawdown: f64) -> f64 {
        let before = self.capital;
        self.capital += stake * (coef - 1.0);
        let worst = (before - stake * drawdown).min(self.capital);
        self.max_drawdown = self.max_drawdown.max(1.0 - worst / self.peak);
        self.peak = self.peak.max(self.capital);
        // SL, как в `server.rs`: множитель TP больше cms, SL — меньше.
        if coef < self.cms {
            self.loss += stake * (1.0 - coef);
            self.losses_in_row += 1;
        } else {
            self.loss = 0.0;
            self.losses_in_row = 0;
        }
        self.capital / before
    }

    /// SL подряд стало `max_losses`: стратегия дальше не тестируется.
    pub fn busted(&self) -> bool {
        self.m.max_losses > 0 && self.losses_in_row >= self.m.max_losses
    }
}

/// [`super::evaluate_trades`] с мартингейлом: `weighted_log` копит логарифмы множителей
/// капитала, просадка — капитала от пика. `None` — серия оборвалась или фильтры не пройдены.
pub fn evaluate_martingale(
    bits: &[u64],
    outcomes: &Outcomes,
    weights: &[f64],
    opens: &[f64],
    tp: f64,
    mut series: Series,
    filters: Filters,
) -> Option<Metrics> {
    let last = outcomes.len() - 1;
    let mut weighted_log = 0.0f64;
    let (mut wins, mut losses, mut total_candles) = (0usize, 0usize, 0i64);

    let mut pos = 0;
    while let Some(open) = next_signal(bits, pos) {
        let close = outcomes.hot[open + 1].close;
        let c = outcomes.cold[open + 1];

        let stake = series.stake(opens[open + 1], tp)?;
        let multiplier = series.settle(stake, c.coef, c.drawdown);
        if series.busted() {
            return None;
        }
        weighted_log = weights[open].mul_add(multiplier.ln(), weighted_log);
        wins += (c.coef > 1.0) as usize;
        losses += (c.coef < 1.0) as usize;
        total_candles += (close as usize).min(last) as i64 - open as i64;

        pos = close as usize;
    }

    finish(
        Tally {
            final_coef: series.capital,
            weighted_log,
            max_drawdown: series.max_drawdown,
            wins,
            losses,
            total_candles,
        },
        filters,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::analyzer::{signal_weights, TpSlClose};

    fn close(a: f64, b: f64) -> bool {
        (a - b).abs() <= 1e-12 * b.abs().max(1.0)
    }

    fn martingale(base: f64, max_losses: usize) -> Martingale {
        Martingale {
            coef: 1.2,
            base,
            max_losses,
        }
    }

    #[test]
    fn stake_covers_series_loss() {
        // TP 2%, SL 1%, база 10%: в долях депозита 1000 это 100 → 160 → 256.
        let mut s = Series::new(martingale(0.1, 0), 0.0);
        let stake = s.stake(100.0, 2.0).unwrap();
        assert!(close(stake, 0.1), "{stake}");
        s.settle(stake, 0.99, 0.01);

        let stake = s.stake(100.0, 2.0).unwrap();
        assert!(close(stake, 0.16), "{stake}");
        s.settle(stake, 0.99, 0.01);

        let stake = s.stake(100.0, 2.0).unwrap();
        assert!(close(stake, 0.256), "{stake}");
        // TP приносит прибыль базы (0.002) и убыток серии × 1.2 (1.2 × 0.0026).
        let m = s.settle(stake, 1.02, 0.005);
        assert!(close(s.capital, 1.00252), "{}", s.capital);
        assert!(close(m, 1.00252 / 0.9974), "{m}");
        // Худшая точка — внутри TP-сделки: 0.9974 − 0.256 × 0.005.
        assert!(close(s.max_drawdown, 0.00388), "{}", s.max_drawdown);

        // После TP база — 10% нового капитала.
        let stake = s.stake(100.0, 2.0).unwrap();
        assert!(close(stake, 0.100252), "{stake}");
    }

    #[test]
    fn series_breaks() {
        let mut s = Series::new(martingale(0.1, 2), 0.0);
        for _ in 0..2 {
            assert!(!s.busted());
            let stake = s.stake(100.0, 2.0).unwrap();
            s.settle(stake, 0.99, 0.01);
        }
        assert!(s.busted());

        // 0.6 → 0.96 → 1.536: третья ставка больше капитала.
        let mut s = Series::new(martingale(0.6, 0), 0.0);
        for _ in 0..2 {
            let stake = s.stake(100.0, 2.0).unwrap();
            s.settle(stake, 0.99, 0.01);
        }
        assert_eq!(s.stake(100.0, 2.0), None);

        // TP 1% не окупает комиссию 1% × 2: серию не отыграть.
        let mut s = Series::new(martingale(0.1, 0), 0.01);
        let stake = s.stake(100.0, 1.0).unwrap();
        s.settle(stake, 0.97, 0.01);
        assert_eq!(s.stake(100.0, 1.0), None);
    }

    #[test]
    fn evaluate_follows_series() {
        // Сигналы на свечах 0 и 2: SL (0.9) на входе 1, потом TP (2.8 = (100 + 180) / 100) на входе 3.
        let closes = TpSlClose {
            indexes: vec![0, 2, 0, 5, 0, 0, 0, 0, 0, 0],
            coefs: vec![1.0, 0.9, 1.0, 2.8, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0],
            drawdowns: vec![0.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0],
        };
        let n = closes.coefs.len();
        let (bits, opens) = ([0b101], vec![100.0; n]);
        let outcomes = Outcomes::from_closings(&closes);
        let weights = signal_weights(n);
        let filters = Filters {
            min_log: std::f64::consts::LN_2,
            min_take_profits: 0,
        };
        let eval = |m| {
            let s = Series::new(m, 0.0);
            evaluate_martingale(&bits, &outcomes, &weights, &opens, 180.0, s, filters)
        };

        let got = eval(martingale(0.5, 0)).expect("passes filters");
        // Ставки 0.5 и 0.5 + 1.2 × 0.05 / 1.8; капитал 0.95, потом 0.95 + 0.96.
        let want_log = weights[0] * 0.95f64.ln() + weights[2] * (1.91f64 / 0.95).ln();
        assert!(close(got.coef, 1.91), "{got:?}");
        assert!(close(got.max_drawdown, 5.0), "{got:?}");
        assert!(close(got.profit_to_dd, 0.91 / 0.05), "{got:?}");
        assert!(close(got.profit_to_candles, 91.0 / 5.0), "{got:?}");
        assert!(
            close(got.weighted_profit_to_dd, (want_log.exp() - 1.0) / 0.05),
            "{got:?}"
        );
        assert_eq!((got.wins, got.losses), (1, 1));

        assert_eq!(eval(martingale(0.5, 1)), None, "first SL ends the series");
    }
}
