//! Горячий цикл перебора. Отличия от эталонного [`super::evaluate`]:
//! - идём сразу от сделки к сделке по таблице «следующий сигнал», а не по всем сигналам;
//! - взвешенный коэффициент `Π cᵢ^wᵢ` считается как `exp(Σ wᵢ·ln cᵢ)` — без `powf` на сделку.
//!
//! Все метрики, кроме `weighted_profit_to_dd`, совпадают бит в бит; он отличается на ~1e-15
//! относительно (проверяет `tests/golden.rs`).

use super::{Metrics, TpSlClose};

/// Нет сигнала до конца истории.
pub const NO_SIGNAL: u32 = u32::MAX;

/// Исход сделки, открытой на свече `i` (AoS-вариант [`TpSlClose`]):
/// всё, что нужно на одну сделку, лежит рядом в памяти.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Outcome {
    /// Свеча закрытия; `len` — не закрылась.
    pub close: u32,
    pub coef: f64,
    pub drawdown: f64,
    /// `ln(coef)` — для взвешенного коэффициента.
    pub ln_coef: f64,
}

impl Outcome {
    pub fn from_closings(c: &TpSlClose) -> Vec<Outcome> {
        c.indexes
            .iter()
            .zip(&c.coefs)
            .zip(&c.drawdowns)
            .map(|((&close, &coef), &drawdown)| Outcome {
                close,
                coef,
                drawdown,
                ln_coef: coef.ln(),
            })
            .collect()
    }
}

/// Вес сделки по индексу свечи сигнала: `exp(i / len)`, свежие сделки весят больше.
pub fn signal_weights(len: usize) -> Vec<f64> {
    (0..len).map(|i| (i as f64 / len as f64).exp()).collect()
}

/// `table[i]` — ближайшая свеча `j >= i` с сигналом `next[j] > current[j]` или [`NO_SIGNAL`].
/// Длина таблицы `len + 1`: индекс закрытия `len` («не закрылась») тоже валиден.
/// Последняя свеча сигнала не даёт — входить было бы не во что.
pub fn fill_next_signal(table: &mut [u32], current: &[f64], next: &[f64]) {
    let len = current.len();
    debug_assert_eq!(table.len(), len + 1);
    table[len] = NO_SIGNAL;
    if len == 0 {
        return;
    }
    table[len - 1] = NO_SIGNAL;
    let mut nearest = NO_SIGNAL;
    for i in (0..len - 1).rev() {
        if next[i] > current[i] {
            nearest = i as u32;
        }
        table[i] = nearest;
    }
}

/// Накопленное состояние оценки одной пары на одном TP/SL.
#[derive(Clone, Copy)]
struct Lane {
    pos: usize,
    final_coef: f64,
    weighted_log: f64,
    max_drawdown: f64,
    wins: usize,
    losses: usize,
    total_candles: i64,
}

impl Lane {
    const NEW: Lane = Lane {
        pos: 0,
        final_coef: 1.0,
        weighted_log: 0.0,
        max_drawdown: 0.0,
        wins: 0,
        losses: 0,
        total_candles: 0,
    };

    /// Учитывает следующую сделку; `false` — сигналов до конца истории больше нет.
    #[inline(always)]
    fn step(&mut self, next_signal: &[u32], outcomes: &[Outcome], weights: &[f64]) -> bool {
        let open = next_signal[self.pos];
        if open == NO_SIGNAL {
            return false;
        }
        let o = outcomes[open as usize + 1];

        self.final_coef *= o.coef;
        self.weighted_log = weights[open as usize].mul_add(o.ln_coef, self.weighted_log);
        if o.drawdown > self.max_drawdown {
            self.max_drawdown = o.drawdown;
        }
        if o.coef > 1.0 {
            self.wins += 1;
        } else if o.coef < 1.0 {
            self.losses += 1;
        }
        // Незакрытая сделка (close == len) считается до последней свечи.
        let close = (o.close as usize).min(outcomes.len() - 1);
        self.total_candles += close as i64 - open as i64;

        self.pos = o.close as usize;
        true
    }

    // `!(x > c)` вместо `x <= c`: NaN должен отбрасываться, как в Go.
    #[allow(clippy::neg_cmp_op_on_partial_ord)]
    fn finish(self, min_take_profits: usize) -> Option<Metrics> {
        if !(self.weighted_log > std::f64::consts::LN_2) {
            return None;
        }
        let weighted_profit_to_dd = (self.weighted_log.exp() - 1.0) / self.max_drawdown;
        if !(weighted_profit_to_dd > 3.0) || self.wins < min_take_profits {
            return None;
        }

        let profit = self.final_coef - 1.0;
        Some(Metrics {
            coef: self.final_coef,
            max_drawdown: self.max_drawdown * 100.0,
            profit_to_dd: if self.max_drawdown == 0.0 {
                f64::INFINITY
            } else {
                profit / self.max_drawdown
            },
            profit_to_candles: if self.total_candles > 0 {
                profit * 100.0 / self.total_candles as f64
            } else {
                0.0
            },
            wins: self.wins,
            losses: self.losses,
            weighted_profit_to_dd,
        })
    }
}

/// То же, что [`super::evaluate`], за один проход по сделкам.
/// `weighted_final_coef > 2` проверяется как `Σ wᵢ·ln cᵢ > ln 2`.
pub fn evaluate_trades(
    next_signal: &[u32],
    outcomes: &[Outcome],
    weights: &[f64],
    min_take_profits: usize,
) -> Option<Metrics> {
    let mut lane = Lane::NEW;
    while lane.step(next_signal, outcomes, weights) {}
    lane.finish(min_take_profits)
}

/// [`evaluate_trades`] для `N` пар на одном TP/SL вперемешку: цепочки зависимых загрузок
/// (сигнал → исход → следующий сигнал) разных пар выполняются параллельно в конвейере CPU.
/// Результат каждой пары тот же, что у [`evaluate_trades`].
#[inline]
pub fn evaluate_trades_interleaved<const N: usize>(
    tables: [&[u32]; N],
    outcomes: &[Outcome],
    weights: &[f64],
    min_take_profits: usize,
) -> [Option<Metrics>; N] {
    let mut lanes = [Lane::NEW; N];
    let mut active = [true; N];
    let mut remaining = N;
    while remaining > 0 {
        for i in 0..N {
            if active[i] && !lanes[i].step(tables[i], outcomes, weights) {
                active[i] = false;
                remaining -= 1;
            }
        }
    }
    lanes.map(|lane| lane.finish(min_take_profits))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::analyzer::evaluate;

    #[test]
    fn next_signal_table() {
        let current = [1.0, 1.0, 1.0, 1.0, 1.0, 1.0];
        let next = [0.0, 2.0, 0.0, 0.0, 2.0, 2.0];
        let mut table = vec![0; current.len() + 1];
        fill_next_signal(&mut table, &current, &next);
        // Сигнал на последней свече (5) не учитывается.
        assert_eq!(table, [1, 1, 4, 4, 4, NO_SIGNAL, NO_SIGNAL]);
    }

    #[test]
    fn matches_reference_on_go_test_case() {
        let closes = TpSlClose {
            indexes: vec![0, 2, 0, 5, 0, 0, 0, 0, 0, 0],
            coefs: vec![1.0, 0.9, 1.0, 2.8, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0],
            drawdowns: vec![0.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0],
        };
        let n = closes.coefs.len();
        let mut next = vec![NO_SIGNAL; n + 1];
        next[..3].copy_from_slice(&[0, 2, 2]);

        let fast = evaluate_trades(
            &next,
            &Outcome::from_closings(&closes),
            &signal_weights(n),
            0,
        );
        let fast = fast.expect("passes filters");
        let reference = evaluate(&[0, 2], &closes, n, 0).expect("passes filters");
        let w = (fast.weighted_profit_to_dd, reference.weighted_profit_to_dd);
        assert!((w.0 - w.1).abs() <= 1e-12 * w.1, "{w:?}");
        let strip = |m: Metrics| Metrics {
            weighted_profit_to_dd: 0.0,
            ..m
        };
        assert_eq!(strip(fast), strip(reference));
    }
}
