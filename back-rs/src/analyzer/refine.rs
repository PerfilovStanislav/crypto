//! Уточнение лучшей стратегии: тот же перебор на мелкой сетке вокруг неё.
//!
//! Каждый параметр берётся в пределах ± шага основной сетки:
//! - TP/SL — с шагом в одну единицу `decimals` (без `decimals` — основной шаг / 10);
//! - коэффициенты индикаторов — с шагом основной / 10, для SMA не меньше 1 (период целый).
//!
//! Значения считаются как `центр + k·шаг`, поэтому исходная лучшая стратегия входит в сетку
//! точно, и уточнённый результат не хуже исходного. Если лучшее значение какого-то параметра
//! оказалось на краю сетки, уточнение повторяется вокруг него (не больше [`MAX_REFINE_ROUNDS`]).

use std::sync::atomic::{AtomicBool, Ordering};

use anyhow::{Context, Result};

use super::{has_enough_closes, Analyzer, Grid, IndicatorParams, Task, TaskResult, TpSlParam};
use crate::indicator::IndicatorType;

/// Предел раундов уточнения, если лучшее значение раз за разом уходит к краю сетки.
pub const MAX_REFINE_ROUNDS: usize = 20;

/// Во сколько раз шаг коэффициентов мельче, чем в основной сетке.
const COEF_SUBDIVISION: f64 = 10.0;

/// Диапазон одного параметра: `values` от `start` до `end` с шагом `step` (в единицах цены
/// для TP/SL, как в `TpSlParam`).
#[derive(Debug, Clone, PartialEq)]
pub struct Span {
    pub values: Vec<f64>,
    pub step: f64,
    /// Нижняя граница допустимых значений: край, упёршийся в неё, двигать некуда.
    pub min: f64,
}

impl Span {
    /// `center + k·step` для `|k·step| <= radius`, кроме значений меньше `min`; центр входит всегда.
    fn around(center: f64, radius: f64, step: f64, min: f64) -> Self {
        let n = (radius / step).round() as i64;
        let values = (-n..=n)
            .map(|k| (k, center + k as f64 * step))
            .filter(|&(k, v)| k == 0 || v >= min)
            .map(|(_, v)| v)
            .collect();
        Span { values, step, min }
    }

    /// `v` — крайнее значение сетки, за которым ещё есть куда двигаться.
    pub fn is_edge(&self, v: f64) -> bool {
        let bits = v.to_bits();
        bits == self.end().to_bits()
            || (bits == self.start().to_bits() && self.start() - self.step >= self.min)
    }

    pub fn start(&self) -> f64 {
        self.values[0]
    }

    pub fn end(&self) -> f64 {
        self.values[self.values.len() - 1]
    }
}

/// Мелкая сетка вокруг лучшей стратегии.
#[derive(Debug, Clone)]
pub struct Refinement {
    pub ind1: IndicatorParams,
    pub ind2: IndicatorParams,
    pub coefs1: Span,
    pub coefs2: Span,
    pub tp: Span,
    pub sl: Span,
    /// Множитель для вывода TP/SL в единицах конфига (`10^decimals`).
    pub(crate) price_factor: f64,
}

impl Refinement {
    /// Параметры `task`, которые лежат на краю этой сетки.
    pub fn edges(&self, task: &Task) -> Vec<&'static str> {
        [
            ("ind1", &self.coefs1, task.ind1.coef),
            ("ind2", &self.coefs2, task.ind2.coef),
            ("tp", &self.tp, task.tp_sl.tp),
            ("sl", &self.sl, task.tp_sl.sl),
        ]
        .into_iter()
        .filter(|(_, span, v)| span.is_edge(*v))
        .map(|(name, _, _)| name)
        .collect()
    }

    /// Две группы индикаторов для `Analyzer::prepare_grid`: варианты ind1 и варианты ind2.
    pub fn groups(&self) -> Vec<Vec<IndicatorParams>> {
        let vary = |p: IndicatorParams, span: &Span| {
            span.values
                .iter()
                .map(|&coef| IndicatorParams { coef, ..p })
                .collect()
        };
        vec![vary(self.ind1, &self.coefs1), vary(self.ind2, &self.coefs2)]
    }
}

/// Колбэк раунда: номер, сетка, её `Grid` и края, из-за которых раунд понадобился.
pub type OnRound<'a> = dyn Fn(usize, &Refinement, &Grid, &[&str]) + 'a;

/// Итог уточнения.
#[derive(Debug, Clone)]
pub struct Refined {
    pub best: TaskResult,
    pub rounds: usize,
    /// Параметры, оставшиеся на краю сетки (только если упёрлись в [`MAX_REFINE_ROUNDS`]).
    pub edges: Vec<&'static str>,
}

impl Analyzer {
    /// Раунды уточнения вокруг `best`, пока лучшее значение на краю сетки. Каждый раунд
    /// учитывает только результаты лучше уже найденного, `on_best` получает лишь улучшения.
    /// `on_round(номер, сетка, её Grid, края прошлого раунда)` вызывается перед перебором.
    pub fn refine(
        &self,
        mut best: TaskResult,
        stop: &AtomicBool,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_round: &OnRound,
    ) -> Result<Refined> {
        let mut edges: Vec<&'static str> = Vec::new();
        for round in 1..=MAX_REFINE_ROUNDS {
            if stop.load(Ordering::Relaxed) {
                break;
            }
            let r = self.refinement(&best)?;
            let grid = self.prepare_grid(&r.groups(), self.refinement_tp_sl(&r));
            on_round(round, &r, &grid, &edges);

            let floor = best.metrics.weighted_profit_to_dd;
            let Some(improved) = self.search_above(&grid, stop, floor, on_best, &|_, _| {}) else {
                // Лучше центра ничего нет — центр не на краю, дальше двигаться незачем.
                return Ok(Refined {
                    best,
                    rounds: round,
                    edges: Vec::new(),
                });
            };
            edges = r.edges(&improved.task);
            best = improved;
            if edges.is_empty() {
                return Ok(Refined {
                    best,
                    rounds: round,
                    edges,
                });
            }
        }
        Ok(Refined {
            best,
            rounds: MAX_REFINE_ROUNDS,
            edges,
        })
    }

    /// Мелкая сетка вокруг `best` по правилам из описания модуля.
    pub fn refinement(&self, best: &TaskResult) -> Result<Refinement> {
        let cfg = &self.cfg;
        let task = &best.task;

        let (unit, price_factor) = if cfg.decimals > 0 {
            let factor = 10f64.powi(cfg.decimals);
            (1.0 / factor, factor)
        } else {
            (0.0, 1.0)
        };
        let price_span = |center: f64, main: f64| {
            let fine = if unit > 0.0 {
                unit.min(main)
            } else {
                main / COEF_SUBDIVISION
            };
            Span::around(center, main, fine, fine)
        };

        let coef_span = |p: &IndicatorParams| -> Result<Span> {
            let main = self.coef_step(p).with_context(|| {
                format!("no indicator config for {} {} {}", p.kind, p.source, p.coef)
            })?;
            Ok(if p.kind == IndicatorType::Sma {
                Span::around(p.coef, main, (main / COEF_SUBDIVISION).max(1.0), 1.0)
            } else {
                let fine = main / COEF_SUBDIVISION;
                Span::around(p.coef, main, fine, fine)
            })
        };

        Ok(Refinement {
            ind1: task.ind1,
            ind2: task.ind2,
            coefs1: coef_span(&task.ind1)?,
            coefs2: coef_span(&task.ind2)?,
            tp: price_span(task.tp_sl.tp, cfg.takeprofit.step),
            sl: price_span(task.tp_sl.sl, cfg.stoploss.step),
            price_factor,
        })
    }

    /// Все TP/SL уточняющей сетки, у которых набирается `minCloses` тейк-профитов.
    pub fn refinement_tp_sl(&self, r: &Refinement) -> Vec<TpSlParam> {
        r.tp.values
            .iter()
            .flat_map(|&tp| r.sl.values.iter().map(move |&sl| TpSlParam { tp, sl }))
            .filter(|&p| has_enough_closes(&self.quotes, p, self.cfg.min_closes))
            .collect()
    }

    /// Шаг коэффициента в основной сетке для индикатора этого типа и источника.
    fn coef_step(&self, p: &IndicatorParams) -> Option<f64> {
        let candidates = || {
            self.cfg
                .indicators
                .iter()
                .flatten()
                .filter(|ind| ind.kind == p.kind && ind.sources.contains(&p.source))
        };
        let tolerance = 1e-9;
        candidates()
            .find(|ind| {
                p.coef >= ind.coefs.start - tolerance && p.coef <= ind.coefs.end + tolerance
            })
            .or_else(|| candidates().next())
            .map(|ind| ind.coefs.step)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::analyzer::{Metrics, Task};
    use crate::config::{AnalyzerConfig, IndicatorConfig, RangeConfig};
    use crate::source::{Quotes, SourceType};

    #[test]
    fn span_keeps_center_exactly() {
        let center = 0.1 + 0.2; // не представимо точно — тем важнее не накапливать шаги
        let s = Span::around(center, 0.1, 0.01, 0.0);
        assert_eq!(s.values.len(), 21);
        assert_eq!(s.values[10].to_bits(), center.to_bits());

        let clipped = Span::around(2.0, 3.0, 1.0, 1.0);
        assert_eq!(clipped.values, [1.0, 2.0, 3.0, 4.0, 5.0]);
    }

    #[test]
    fn edges_ignore_the_hard_minimum() {
        let s = Span::around(5.0, 2.0, 1.0, 1.0); // 3..7
        assert!(s.is_edge(3.0) && s.is_edge(7.0));
        assert!(!s.is_edge(5.0) && !s.is_edge(4.0));

        let clipped = Span::around(2.0, 3.0, 1.0, 1.0); // 1..5, снизу упёрлись в минимум
        assert!(
            !clipped.is_edge(1.0),
            "below the minimum there is nowhere to go"
        );
        assert!(clipped.is_edge(5.0));
    }

    #[test]
    fn steps_follow_the_rules() {
        let range = |start, end, step| RangeConfig { start, end, step };
        let ind = |kind, coefs| IndicatorConfig {
            kind,
            coefs,
            sources: vec![SourceType::O, SourceType::L],
        };
        // Как SOLUSDT: decimals 2, TP/SL в конфиге шагом 10 (после деления — 0.1).
        let cfg = AnalyzerConfig {
            decimals: 2,
            takeprofit: range(9.0, 15.0, 0.1),
            stoploss: range(5.0, 15.0, 0.1),
            indicators: vec![
                vec![ind(IndicatorType::Tema, range(0.2, 1.4, 0.1))],
                vec![ind(IndicatorType::Sma, range(3.0, 15.0, 1.0))],
            ],
            ..Default::default()
        };
        let az = Analyzer::with_cutoff_index(cfg, Quotes::default(), 0);
        let best = TaskResult {
            task: Task {
                ind1: IndicatorParams {
                    kind: IndicatorType::Tema,
                    source: SourceType::O,
                    coef: 1.3,
                },
                ind2: IndicatorParams {
                    kind: IndicatorType::Sma,
                    source: SourceType::L,
                    coef: 3.0,
                },
                tp_sl: TpSlParam { tp: 10.6, sl: 6.0 },
                timeframe: "4h".into(),
                decimals: 2,
            },
            metrics: Metrics {
                coef: 1.0,
                max_drawdown: 0.0,
                profit_to_dd: 0.0,
                profit_to_candles: 0.0,
                wins: 0,
                losses: 0,
                weighted_profit_to_dd: 0.0,
            },
        };

        let r = az.refinement(&best).unwrap();
        let close = |a: f64, b: f64| (a - b).abs() < 1e-9;
        // TP 1050..1070 шагом 1 (в единицах цены 10.5..10.7 шагом 0.01).
        assert_eq!(r.tp.values.len(), 21);
        assert!(close(r.tp.start(), 10.5) && close(r.tp.end(), 10.7) && close(r.tp.step, 0.01));
        assert!(close(r.sl.start(), 5.9) && close(r.sl.end(), 6.1));
        // tema 1.2..1.4 шагом 0.01.
        assert!(close(r.coefs1.start(), 1.2) && close(r.coefs1.end(), 1.4));
        assert!(close(r.coefs1.step, 0.01));
        // SMA: период целый, 2..4, но не меньше 1.
        assert_eq!(r.coefs2.values, [2.0, 3.0, 4.0]);
    }
}
