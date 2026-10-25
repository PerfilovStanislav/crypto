//! Горячий цикл перебора. Отличия от эталонного [`super::evaluate`]:
//! - идём сразу от сделки к сделке по битовой маске сигналов, а не по всем сигналам;
//! - взвешенный коэффициент `Π cᵢ^wᵢ` считается как `exp(Σ wᵢ·ln cᵢ)` — без `powf` на сделку;
//! - сначала дешёвый проход [`scout`] по 16-байтовым [`HotOutcome`] (исходы одного TP/SL
//!   помещаются в L1) даёт верхнюю оценку weighted pr/dd, и только кандидаты, которые могут
//!   стать новым максимумом, пересчитываются точно в [`evaluate_trades`].
//!
//! Все метрики, кроме `weighted_profit_to_dd`, совпадают с эталоном бит в бит; он отличается
//! на ~1e-15 относительно (проверяет `tests/golden.rs`).

use super::{Metrics, TpSlClose};

/// Горячая часть исхода сделки, открытой на свече `i`: 16 байт.
#[derive(Debug, Clone, Copy, PartialEq)]
#[repr(C)]
pub struct HotOutcome {
    /// `ln(coef)`: и для взвешенного коэффициента, и как признак TP (`> 0` ⇔ `coef > 1`).
    pub ln_coef: f64,
    /// Просадка, округлённая вниз до f32: оценка pr/dd по ней не меньше точной.
    pub drawdown_floor: f32,
    /// Свеча закрытия; `len` — не закрылась.
    pub close: u32,
}

/// Точная часть исхода — нужна только при пересчёте кандидатов.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ColdOutcome {
    pub coef: f64,
    pub drawdown: f64,
}

/// Исходы сделок одного TP/SL по свече входа (AoS-вариант [`TpSlClose`]).
#[derive(Debug, Clone, Default)]
pub struct Outcomes {
    pub hot: Vec<HotOutcome>,
    pub cold: Vec<ColdOutcome>,
}

impl Outcomes {
    pub fn from_closings(c: &TpSlClose) -> Self {
        let mut out = Outcomes::default();
        for ((&close, &coef), &drawdown) in c.indexes.iter().zip(&c.coefs).zip(&c.drawdowns) {
            let ln_coef = coef.ln();
            // На этом держится подсчёт тейк-профитов в `scout`; log монотонен и ln(1) = 0.
            assert_eq!(ln_coef > 0.0, coef > 1.0, "ln({coef}) = {ln_coef}");
            out.hot.push(HotOutcome {
                ln_coef,
                drawdown_floor: f32_floor(drawdown),
                close,
            });
            out.cold.push(ColdOutcome { coef, drawdown });
        }
        out
    }

    pub fn len(&self) -> usize {
        self.hot.len()
    }

    pub fn is_empty(&self) -> bool {
        self.hot.is_empty()
    }
}

/// Пороги отбора стратегий из конфига.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Filters {
    /// `ln(minWeightedCoef)`: взвешенный коэффициент должен быть строго больше порога.
    pub min_log: f64,
    pub min_take_profits: usize,
}

/// Наибольшее f32, не превосходящее `x`.
pub(super) fn f32_floor(x: f64) -> f32 {
    let f = x as f32;
    if f as f64 <= x || f.is_nan() {
        return f;
    }
    // Шаг вниз на один ulp.
    match f {
        0.0 => -f32::from_bits(1),
        f if f > 0.0 => f32::from_bits(f.to_bits() - 1),
        f => f32::from_bits(f.to_bits() + 1),
    }
}

/// Вес сделки по индексу свечи сигнала: `exp(i / len)`, свежие сделки весят больше.
pub fn signal_weights(len: usize) -> Vec<f64> {
    (0..len).map(|i| (i as f64 / len as f64).exp()).collect()
}

/// Сколько `u64` нужно на набор сигналов для `len` свечей (бит `len` тоже адресуем).
pub fn signal_words(len: usize) -> usize {
    len / 64 + 1
}

/// Набор сигналов битами: бит `i` — `next[i] > current[i]`; последняя свеча не в счёт —
/// входить было бы не во что.
pub fn fill_signal_bits(bits: &mut [u64], current: &[f64], next: &[f64]) {
    let last = current.len().saturating_sub(1);
    debug_assert_eq!(bits.len(), signal_words(current.len()));
    for (w, word) in bits.iter_mut().enumerate() {
        let start = (w * 64).min(last);
        let end = (start + 64).min(last);
        *word = current[start..end]
            .iter()
            .zip(&next[start..end])
            .enumerate()
            .fold(0, |acc, (j, (c, n))| acc | (((n > c) as u64) << j));
    }
}

/// Первая свеча `>= pos` с сигналом. Сигналы плотные, так что обычно хватает одного слова.
#[inline(always)]
pub(super) fn next_signal(bits: &[u64], pos: usize) -> Option<usize> {
    let mut w = pos / 64;
    let mut word = *bits.get(w)? & (u64::MAX << (pos % 64));
    loop {
        if word != 0 {
            return Some(w * 64 + word.trailing_zeros() as usize);
        }
        w += 1;
        word = *bits.get(w)?;
    }
}

/// Быстрый проход: ровно тот же путь по сделкам и тот же `weighted_log`, что у точного,
/// но без лишних полей.
#[derive(Clone, Copy)]
struct Scout {
    pos: usize,
    weighted_log: f64,
    drawdown_floor: f32,
    wins: u32,
}

impl Scout {
    const NEW: Scout = Scout {
        pos: 0,
        weighted_log: 0.0,
        drawdown_floor: 0.0,
        wins: 0,
    };

    #[inline(always)]
    fn step(&mut self, bits: &[u64], hot: &[HotOutcome], weights: &[f64]) -> bool {
        let Some(open) = next_signal(bits, self.pos) else {
            return false;
        };
        let o = hot[open + 1];
        self.weighted_log = weights[open].mul_add(o.ln_coef, self.weighted_log);
        // Без ветвлений: TP и SL чередуются почти случайно, предсказатель переходов тут бессилен.
        self.drawdown_floor = self.drawdown_floor.max(o.drawdown_floor);
        self.wins += (o.ln_coef > 0.0) as u32;
        self.pos = o.close as usize;
        true
    }

    // `!(x > c)` вместо `x <= c`: NaN должен отбрасываться, как в Go.
    #[allow(clippy::neg_cmp_op_on_partial_ord)]
    fn bound(self, f: Filters) -> Option<f64> {
        if !(self.weighted_log > f.min_log) || (self.wins as usize) < f.min_take_profits {
            return None;
        }
        // Числитель тот же, что в точном расчёте, знаменатель не больше — оценка не меньше.
        let bound = (self.weighted_log.exp() - 1.0) / self.drawdown_floor as f64;
        (bound > 3.0).then_some(bound)
    }
}

/// Верхняя оценка `weighted_profit_to_dd` пары на этом TP/SL или `None`, если пара точно
/// не пройдёт фильтры. Если [`evaluate_trades`] даёт `Some(m)`, здесь `Some(b)` и
/// `b >= m.weighted_profit_to_dd`.
pub fn scout(bits: &[u64], hot: &[HotOutcome], weights: &[f64], filters: Filters) -> Option<f64> {
    let mut s = Scout::NEW;
    while s.step(bits, hot, weights) {}
    s.bound(filters)
}

/// [`scout`] для `N` пар вперемешку: цепочки зависимых загрузок (сигнал → исход → следующий
/// сигнал) разных пар выполняются параллельно в конвейере CPU.
#[inline]
pub fn scout_interleaved<const N: usize>(
    bits: [&[u64]; N],
    hot: &[HotOutcome],
    weights: &[f64],
    filters: Filters,
) -> [Option<f64>; N] {
    let mut lanes = [Scout::NEW; N];
    let mut active = [true; N];
    let mut remaining = N;
    while remaining > 0 {
        for i in 0..N {
            if active[i] && !lanes[i].step(bits[i], hot, weights) {
                active[i] = false;
                remaining -= 1;
            }
        }
    }
    lanes.map(|s| s.bound(filters))
}

/// Точный расчёт — то же, что [`super::evaluate`], за один проход по сделкам.
/// `weighted_final_coef > minWeightedCoef` проверяется как `Σ wᵢ·ln cᵢ > ln(minWeightedCoef)`.
pub fn evaluate_trades(
    bits: &[u64],
    outcomes: &Outcomes,
    weights: &[f64],
    filters: Filters,
) -> Option<Metrics> {
    let last = outcomes.len() - 1;
    let mut final_coef = 1.0;
    let mut weighted_log = 0.0f64;
    let mut max_drawdown = 0.0f64;
    let (mut wins, mut losses, mut total_candles) = (0usize, 0usize, 0i64);

    let mut pos = 0;
    while let Some(open) = next_signal(bits, pos) {
        let h = outcomes.hot[open + 1];
        let c = outcomes.cold[open + 1];

        final_coef *= c.coef;
        weighted_log = weights[open].mul_add(h.ln_coef, weighted_log);
        max_drawdown = max_drawdown.max(c.drawdown);
        wins += (c.coef > 1.0) as usize;
        losses += (c.coef < 1.0) as usize;
        // Незакрытая сделка (close == len) считается до последней свечи.
        total_candles += (h.close as usize).min(last) as i64 - open as i64;

        pos = h.close as usize;
    }

    finish(
        Tally {
            final_coef,
            weighted_log,
            max_drawdown,
            wins,
            losses,
            total_candles,
        },
        filters,
    )
}

/// Итоги прохода по сделкам стратегии.
pub(super) struct Tally {
    pub final_coef: f64,
    pub weighted_log: f64,
    pub max_drawdown: f64,
    pub wins: usize,
    pub losses: usize,
    pub total_candles: i64,
}

/// Фильтры (weighted coef, weighted pr/dd > 3, число TP) и метрики по итогам прохода.
// `!(x > c)` вместо `x <= c`: NaN должен отбрасываться, как в Go.
#[allow(clippy::neg_cmp_op_on_partial_ord)]
pub(super) fn finish(t: Tally, f: Filters) -> Option<Metrics> {
    if !(t.weighted_log > f.min_log) {
        return None;
    }
    let weighted_profit_to_dd = (t.weighted_log.exp() - 1.0) / t.max_drawdown;
    if !(weighted_profit_to_dd > 3.0) || t.wins < f.min_take_profits {
        return None;
    }

    let profit = t.final_coef - 1.0;
    Some(Metrics {
        coef: t.final_coef,
        max_drawdown: t.max_drawdown * 100.0,
        profit_to_dd: if t.max_drawdown == 0.0 {
            f64::INFINITY
        } else {
            profit / t.max_drawdown
        },
        profit_to_candles: if t.total_candles > 0 {
            profit * 100.0 / t.total_candles as f64
        } else {
            0.0
        },
        wins: t.wins,
        losses: t.losses,
        weighted_profit_to_dd,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::analyzer::evaluate;

    #[test]
    fn signal_bits_and_next_signal() {
        let len = 150;
        let current: Vec<f64> = (0..len).map(|i| (i as f64 * 0.37).sin()).collect();
        let next: Vec<f64> = (0..len).map(|i| (i as f64 * 0.21).cos()).collect();
        let mut bits = vec![0; signal_words(len)];
        fill_signal_bits(&mut bits, &current, &next);

        for pos in 0..=len {
            // Сигнал на последней свече не учитывается.
            let want = (pos..len - 1).find(|&i| next[i] > current[i]);
            assert_eq!(next_signal(&bits, pos), want, "pos {pos}");
        }
    }

    #[test]
    fn f32_floor_never_exceeds_input() {
        for x in [
            0.0,
            1e-300,
            0.1,
            0.123456789,
            1.0 / 3.0,
            7.99e-2,
            1e30,
            -0.1,
            -1e-300,
        ] {
            let f = f32_floor(x);
            assert!(f as f64 <= x, "{x} -> {f}");
            assert!(f.next_up() as f64 > x, "{x} -> {f} is not the closest");
        }
    }

    #[test]
    fn matches_reference_on_go_test_case() {
        let closes = TpSlClose {
            indexes: vec![0, 2, 0, 5, 0, 0, 0, 0, 0, 0],
            coefs: vec![1.0, 0.9, 1.0, 2.8, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0],
            drawdowns: vec![0.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0],
        };
        let n = closes.coefs.len();
        let bits = [0b101]; // сигналы на свечах 0 и 2
        let outcomes = Outcomes::from_closings(&closes);
        let weights = signal_weights(n);
        let filters = Filters {
            min_log: std::f64::consts::LN_2,
            min_take_profits: 0,
        };

        let fast = evaluate_trades(&bits, &outcomes, &weights, filters).expect("passes filters");
        let reference = evaluate(&[0, 2], &closes, n, 0).expect("passes filters");
        let w = (fast.weighted_profit_to_dd, reference.weighted_profit_to_dd);
        assert!((w.0 - w.1).abs() <= 1e-12 * w.1, "{w:?}");
        let strip = |m: Metrics| Metrics {
            weighted_profit_to_dd: 0.0,
            ..m
        };
        assert_eq!(strip(fast), strip(reference));

        let bound =
            scout(&bits, &outcomes.hot, &weights, filters).expect("scout keeps the candidate");
        assert!(bound >= fast.weighted_profit_to_dd);
    }
}
