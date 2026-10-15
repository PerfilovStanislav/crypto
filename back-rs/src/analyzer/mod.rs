//! Перебор стратегий «индикатор1 × индикатор2 × TP/SL» (порт `back/src/analyzer`).
//!
//! Сигнал на свече `i` — `ind2[i] > ind1[i]`. Вход по открытию свечи `i + 1`,
//! если предыдущая сделка уже закрыта; выход — по TP/SL из [`TpSlClose`].

mod closings;
mod fast;
mod format;

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::Mutex;

use anyhow::{bail, Result};
use chrono::{Months, Utc};
use rayon::prelude::*;

pub use closings::{calculate_closings, has_enough_closes, TpSlClose, TpSlParam};
pub use fast::{
    evaluate_trades, evaluate_trades_interleaved, fill_next_signal, signal_weights, Outcome,
    NO_SIGNAL,
};

use crate::config::{AnalyzerConfig, RangeConfig};
use crate::indicator::IndicatorType;
use crate::source::{Quotes, SourceType};

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct IndicatorParams {
    pub kind: IndicatorType,
    pub source: SourceType,
    pub coef: f64,
}

#[derive(Debug, Clone)]
pub struct Task {
    pub ind1: IndicatorParams,
    pub ind2: IndicatorParams,
    pub tp_sl: TpSlParam,
    pub timeframe: String,
    pub decimals: i32,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Metrics {
    /// Итоговый множитель капитала.
    pub coef: f64,
    /// Максимальная просадка одной сделки, %.
    pub max_drawdown: f64,
    pub profit_to_dd: f64,
    /// Прибыль в % на свечу в позиции.
    pub profit_to_candles: f64,
    pub wins: usize,
    pub losses: usize,
    /// Критерий отбора: прибыль с весом, растущим к свежим сделкам, делённая на просадку.
    pub weighted_profit_to_dd: f64,
}

#[derive(Debug, Clone)]
pub struct TaskResult {
    pub task: Task,
    pub metrics: Metrics,
}

/// Пара индикаторов (индексы в [`Grid::params`]), прошедшая фильтры по числу сигналов.
/// Сами сигналы не хранятся: для ~10⁶ пар это гигабайты, а пересчёт стоит `len` сравнений.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Compare {
    pub ind1: usize,
    pub ind2: usize,
}

/// Всё, что считается до перебора: значения индикаторов, исходы TP/SL и пары.
#[derive(Debug, Default)]
pub struct Grid {
    pub params: Vec<IndicatorParams>,
    /// `prices[i]` — значения индикатора `params[i]`.
    pub prices: Vec<Vec<f64>>,
    /// Группы из конфига; пары строятся между соседними группами.
    pub groups: Vec<Vec<usize>>,
    pub tp_sl: Vec<(TpSlParam, Vec<Outcome>)>,
    pub compares: Vec<Compare>,
    /// Веса сделок по свече сигнала, см. [`signal_weights`].
    pub weights: Vec<f64>,
}

pub struct Analyzer {
    pub cfg: AnalyzerConfig,
    pub quotes: Quotes,
    ln: usize,
    cutoff_index: usize,
}

impl Analyzer {
    pub fn new(cfg: AnalyzerConfig, quotes: Quotes) -> Self {
        let cutoff = Utc::now()
            .checked_sub_months(Months::new(cfg.last_months_limit))
            .map_or(i64::MIN, |t| t.timestamp());
        let cutoff_index = quotes
            .timestamps
            .iter()
            .position(|&t| t >= cutoff)
            .unwrap_or(quotes.len());
        Self::with_cutoff_index(cfg, quotes, cutoff_index)
    }

    /// `cutoff_index` — первая свеча периода `lastMonthsLimit`; нужен тестам, чтобы не зависеть от часов.
    pub fn with_cutoff_index(cfg: AnalyzerConfig, quotes: Quotes, cutoff_index: usize) -> Self {
        Self {
            ln: quotes.len(),
            cfg,
            quotes,
            cutoff_index,
        }
    }

    pub fn len(&self) -> usize {
        self.ln
    }

    pub fn is_empty(&self) -> bool {
        self.ln == 0
    }

    pub fn indicator_prices(&self, kind: IndicatorType, source: SourceType, coef: f64) -> Vec<f64> {
        kind.calculate(&self.quotes.prices(source), coef)
    }

    /// Индексы свечей, где `next > current`, или `None`, если сигналов меньше порогов из конфига.
    pub fn compare_indicators(&self, current: &[f64], next: &[f64]) -> Option<Vec<u32>> {
        if !self.enough_signals(current, next) {
            return None;
        }
        let last = self.ln.saturating_sub(1);
        Some(
            (0..last)
                .filter(|&i| next[i] > current[i])
                .map(|i| i as u32)
                .collect(),
        )
    }

    /// Фильтры `minSignals` и `minSignalsLastMonths` (сигнал на последней свече не считается).
    fn enough_signals(&self, current: &[f64], next: &[f64]) -> bool {
        let last = self.ln.saturating_sub(1);
        let cutoff = self.cutoff_index.min(last);
        let count = |r: std::ops::Range<usize>| {
            current[r.clone()]
                .iter()
                .zip(&next[r])
                .map(|(c, n)| (n > c) as usize)
                .sum::<usize>()
        };
        let recent = count(cutoff..last);
        let all = count(0..cutoff) + recent;
        all >= self.cfg.min_signals && recent >= self.cfg.min_signals_last_months
    }

    pub fn prepare(&self) -> Result<Grid> {
        let mut grid = Grid {
            weights: signal_weights(self.ln),
            ..Grid::default()
        };
        self.fill_indicator_params(&mut grid)?;
        self.fill_tp_sl(&mut grid)?;
        self.fill_compares(&mut grid);
        Ok(grid)
    }

    fn fill_indicator_params(&self, grid: &mut Grid) -> Result<()> {
        // Go-версия держала map[IndicatorParams]; ключ с f64 заменён на биты coef.
        let mut index: HashMap<(IndicatorType, SourceType, u64), usize> = HashMap::new();

        for group in &self.cfg.indicators {
            let mut ids = Vec::new();
            for ind in group {
                for &source in &ind.sources {
                    for coef in float_range(ind.coefs)? {
                        let id = *index
                            .entry((ind.kind, source, coef.to_bits()))
                            .or_insert_with(|| {
                                grid.params.push(IndicatorParams {
                                    kind: ind.kind,
                                    source,
                                    coef,
                                });
                                grid.params.len() - 1
                            });
                        ids.push(id);
                    }
                }
            }
            grid.groups.push(ids);
        }

        let sources: HashSet<SourceType> = grid.params.iter().map(|p| p.source).collect();
        let source_prices: HashMap<SourceType, Vec<f64>> = sources
            .into_iter()
            .map(|s| (s, self.quotes.prices(s)))
            .collect();

        grid.prices = grid
            .params
            .par_iter()
            .map(|p| p.kind.calculate(&source_prices[&p.source], p.coef))
            .collect();
        Ok(())
    }

    fn fill_tp_sl(&self, grid: &mut Grid) -> Result<()> {
        let tps: Vec<f64> = float_range(self.cfg.takeprofit)?.collect();
        let sls: Vec<f64> = float_range(self.cfg.stoploss)?.collect();

        // Как в Go: по росту SL идём, пока набирается minCloses тейк-профитов.
        let params: Vec<TpSlParam> = tps
            .par_iter()
            .flat_map_iter(|&tp| {
                sls.iter()
                    .map(move |&sl| TpSlParam { tp, sl })
                    .take_while(|&p| has_enough_closes(&self.quotes, p, self.cfg.min_closes))
            })
            .collect();

        grid.tp_sl = params
            .into_par_iter()
            .map(|p| {
                let closes = calculate_closings(&self.quotes, p, self.cfg.commission);
                (p, Outcome::from_closings(&closes))
            })
            .collect();
        Ok(())
    }

    fn fill_compares(&self, grid: &mut Grid) {
        let mut seen = HashSet::new();
        let mut pairs = Vec::new();
        for w in grid.groups.windows(2) {
            for &a in &w[0] {
                for &b in &w[1] {
                    if seen.insert((a, b)) {
                        pairs.push((a, b));
                    }
                }
            }
        }

        let prices = &grid.prices;
        grid.compares = pairs
            .into_par_iter()
            .filter(|&(a, b)| self.enough_signals(&prices[a], &prices[b]))
            .map(|(ind1, ind2)| Compare { ind1, ind2 })
            .collect();
    }

    /// Перебирает все пары × TP/SL на текущем пуле rayon. `on_best` вызывается на каждый
    /// новый глобальный максимум `weighted_profit_to_dd`, `on_progress(done, total)` — по
    /// мере обработки пар. Возвращает лучший результат.
    ///
    /// Пары идут блоками по [`PAIRS_PER_BLOCK`]: исходы одного TP/SL (~100 КБ) читаются
    /// из памяти один раз на блок, а не на каждую пару.
    pub fn search(
        &self,
        grid: &Grid,
        stop: &AtomicBool,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_progress: &(dyn Fn(usize, usize) + Sync),
    ) -> Option<TaskResult> {
        let max_bits = AtomicU64::new(0f64.to_bits());
        let best: Mutex<Option<TaskResult>> = Mutex::new(None);
        let completed = AtomicUsize::new(0);
        let total = grid.compares.len();
        let stride = self.ln + 1;
        let min_tp = self.cfg.min_take_profits;

        let report = |cmp: &Compare, param: &TpSlParam, metrics: Metrics| {
            let current = f64::from_bits(max_bits.load(Ordering::Relaxed));
            if metrics.weighted_profit_to_dd <= current
                || !update_max(&max_bits, metrics.weighted_profit_to_dd)
            {
                return;
            }
            let result = TaskResult {
                task: Task {
                    ind1: grid.params[cmp.ind1],
                    ind2: grid.params[cmp.ind2],
                    tp_sl: *param,
                    timeframe: self.cfg.timeframe.clone(),
                    decimals: self.cfg.decimals,
                },
                metrics,
            };
            on_best(&result);

            let mut best = best.lock().unwrap_or_else(|e| e.into_inner());
            let better = best.as_ref().map_or(true, |b| {
                b.metrics.weighted_profit_to_dd < metrics.weighted_profit_to_dd
            });
            if better {
                *best = Some(result);
            }
        };

        grid.compares.par_chunks(PAIRS_PER_BLOCK).for_each_init(
            Vec::new,
            |tables: &mut Vec<u32>, block| {
                if stop.load(Ordering::Relaxed) {
                    return;
                }
                tables.resize(block.len() * stride, NO_SIGNAL);
                for (cmp, table) in block.iter().zip(tables.chunks_exact_mut(stride)) {
                    fill_next_signal(table, &grid.prices[cmp.ind1], &grid.prices[cmp.ind2]);
                }

                for (param, outcomes) in &grid.tp_sl {
                    let mut lanes = block
                        .chunks_exact(LANES)
                        .zip(tables.chunks_exact(LANES * stride));
                    for (cmps, tabs) in lanes.by_ref() {
                        let tabs: [&[u32]; LANES] =
                            std::array::from_fn(|i| &tabs[i * stride..][..stride]);
                        let results =
                            evaluate_trades_interleaved(tabs, outcomes, &grid.weights, min_tp);
                        for (cmp, metrics) in cmps.iter().zip(results) {
                            if let Some(metrics) = metrics {
                                report(cmp, param, metrics);
                            }
                        }
                    }
                    let tail = block.len() / LANES * LANES;
                    for (cmp, table) in block[tail..]
                        .iter()
                        .zip(tables[tail * stride..].chunks_exact(stride))
                    {
                        if let Some(metrics) =
                            evaluate_trades(table, outcomes, &grid.weights, min_tp)
                        {
                            report(cmp, param, metrics);
                        }
                    }
                }
                let done = completed.fetch_add(block.len(), Ordering::Relaxed) + block.len();
                on_progress(done, total);
            },
        );

        best.into_inner().unwrap_or_else(|e| e.into_inner())
    }

    /// Полный прогон с выводом в консоль, как `Analyzer.Run` в Go.
    pub fn run(&self, stop: &AtomicBool) -> Result<Option<TaskResult>> {
        let pool = rayon::ThreadPoolBuilder::new()
            .num_threads(self.cfg.threads)
            .build()?;

        pool.install(|| {
            let grid = self.prepare()?;
            tracing::info!(
                params = grid.params.len(),
                tp_sl = grid.tp_sl.len(),
                pairs = grid.compares.len(),
                "grid prepared"
            );

            println!("Jobs:{}", grid.compares.len() * grid.tp_sl.len());
            println!("{}", format::header());

            let printed: [AtomicBool; 11] = Default::default();
            let progress = |done: usize, total: usize| {
                let pct = done * 10 / total;
                if pct == 0 || pct > 10 || printed[pct].swap(true, Ordering::Relaxed) {
                    return;
                }
                let bar: String = (0..10).map(|i| if i < pct { '█' } else { '░' }).collect();
                println!("Progress: [{bar}] {}% ({done}/{total} jobs)", pct * 10);
            };

            Ok(self.search(&grid, stop, &|r: &TaskResult| println!("{r}"), &progress))
        })
    }
}

/// Сколько пар обрабатывается вместе на одном TP/SL (см. [`Analyzer::search`]).
pub const PAIRS_PER_BLOCK: usize = 64;

/// Сколько пар оценивается вперемешку, см. [`evaluate_trades_interleaved`].
const LANES: usize = 2;

/// Оценка одной стратегии — эталонный порт `testTaskDirect` из Go. `None` — не прошла фильтры
/// (weighted coef > 2, weighted pr/dd > 3, тейк-профитов не меньше `min_take_profits`).
/// В переборе используется равная ей по результату [`evaluate_trades`].
// `!(x > c)` вместо `x <= c`: NaN должен отбрасываться, как в Go.
#[allow(clippy::neg_cmp_op_on_partial_ord)]
pub fn evaluate(
    signals: &[u32],
    closes: &TpSlClose,
    ln: usize,
    min_take_profits: usize,
) -> Option<Metrics> {
    let mut final_coef = 1.0;
    let mut weighted_final_coef = 1.0;
    // В Go стартовое значение -1; индексы сигналов неотрицательны, так что 0 эквивалентен.
    let mut closing_index = 0u32;
    let mut max_drawdown = 0.0;

    for &open in signals {
        if open < closing_index {
            continue;
        }
        let next = open as usize + 1;

        let trade_coef = closes.coefs[next];
        final_coef *= trade_coef;
        closing_index = closes.indexes[next];
        let drawdown = closes.drawdowns[next];

        let weight = (open as f64 / ln as f64).exp();
        weighted_final_coef *= trade_coef.powf(weight);

        if drawdown > max_drawdown {
            max_drawdown = drawdown;
        }
    }

    if !(weighted_final_coef > 2.0) {
        return None;
    }
    let weighted_profit_to_dd = (weighted_final_coef - 1.0) / max_drawdown;
    if !(weighted_profit_to_dd > 3.0) {
        return None;
    }

    let mut wins = 0;
    let mut losses = 0;
    let mut total_candles: i64 = 0;
    let mut c_index = 0u32;
    for &open in signals {
        if open < c_index {
            continue;
        }
        let next = open as usize + 1;
        let trade_coef = closes.coefs[next];
        c_index = closes.indexes[next];

        if trade_coef > 1.0 {
            wins += 1;
        } else if trade_coef < 1.0 {
            losses += 1;
        }

        let mut actual_close = c_index as i64;
        if actual_close == closes.coefs.len() as i64 {
            actual_close -= 1;
        }
        total_candles += actual_close - open as i64;
    }

    if wins < min_take_profits {
        return None;
    }

    let profit_pct = (final_coef - 1.0) * 100.0;
    let profit_to_candles = if total_candles > 0 {
        profit_pct / total_candles as f64
    } else {
        0.0
    };
    let profit_to_dd = if max_drawdown == 0.0 {
        f64::INFINITY
    } else {
        (final_coef - 1.0) / max_drawdown
    };

    Some(Metrics {
        coef: final_coef,
        max_drawdown: max_drawdown * 100.0,
        profit_to_dd,
        profit_to_candles,
        wins,
        losses,
        weighted_profit_to_dd,
    })
}

/// CAS-цикл: записывает `val`, если он больше текущего максимума.
fn update_max(bits: &AtomicU64, val: f64) -> bool {
    let mut cur = bits.load(Ordering::Relaxed);
    loop {
        if val <= f64::from_bits(cur) {
            return false;
        }
        match bits.compare_exchange_weak(cur, val.to_bits(), Ordering::Relaxed, Ordering::Relaxed) {
            Ok(_) => return true,
            Err(actual) => cur = actual,
        }
    }
}

/// `for v := start; v <= end; v += step` из Go — с тем же накоплением ошибки,
/// чтобы сетка параметров совпадала до бита.
#[allow(clippy::neg_cmp_op_on_partial_ord)] // NaN-шаг тоже ошибка
fn float_range(r: RangeConfig) -> Result<impl Iterator<Item = f64>> {
    if !(r.step > 0.0) {
        bail!("range step must be positive, got {r:?}");
    }
    Ok(
        std::iter::successors(Some(r.start), move |v| Some(v + r.step))
            .take_while(move |&v| v <= r.end),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::IndicatorConfig;

    #[test]
    fn evaluate_metrics() {
        let coefs = vec![1.0, 0.9, 1.0, 2.8, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0];
        let closes = TpSlClose {
            indexes: vec![0, 2, 0, 5, 0, 0, 0, 0, 0, 0],
            drawdowns: vec![0.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0],
            coefs,
        };
        // Сделка 1: сигнал 0, вход на 1, выход на 2, coef 0.9.
        // Сделка 2: сигнал 2, вход на 3, выход на 5, coef 2.8.
        let m = evaluate(&[0, 2], &closes, 10, 0).expect("strategy passes filters");

        let close = |a: f64, b: f64| (a - b).abs() < 1e-9;
        assert!(close(m.coef, 0.9 * 2.8), "coef {}", m.coef);
        assert!(close(m.max_drawdown, 10.0), "max dd {}", m.max_drawdown);
        assert!(close(m.profit_to_dd, 15.2), "pr/dd {}", m.profit_to_dd);
        // 152% на (2 - 0) + (5 - 2) = 5 свечей.
        assert!(
            close(m.profit_to_candles, 30.4),
            "pr/cnd {}",
            m.profit_to_candles
        );
        assert_eq!((m.wins, m.losses), (1, 1));
    }

    #[test]
    fn run_completes_on_small_grid() {
        let range = |v: f64| RangeConfig {
            start: v,
            end: v,
            step: 1.0,
        };
        let cfg = AnalyzerConfig {
            threads: 4,
            pair: "BTC/USDT".into(),
            timeframe: "1h".into(),
            commission: 0.001,
            min_closes: 1,
            min_signals: 1,
            takeprofit: range(10.0),
            stoploss: range(10.0),
            indicators: vec![
                vec![IndicatorConfig {
                    kind: IndicatorType::Sma,
                    coefs: range(5.0),
                    sources: vec![SourceType::C],
                }],
                vec![IndicatorConfig {
                    kind: IndicatorType::Sma,
                    coefs: range(10.0),
                    sources: vec![SourceType::C],
                }],
            ],
            ..Default::default()
        };

        let n = 100;
        let now = Utc::now().timestamp();
        let opens: Vec<f64> = (0..n).map(|i| 100.0 + i as f64).collect();
        let quotes = Quotes {
            timestamps: (0..n).map(|i| now + i as i64 * 3600).collect(),
            highs: opens.iter().map(|o| o + 15.0).collect(),
            lows: opens.iter().map(|o| o - 15.0).collect(),
            closes: opens.iter().map(|o| o + 2.0).collect(),
            volumes: vec![1000.0; n],
            opens,
        };

        Analyzer::new(cfg, quotes)
            .run(&AtomicBool::new(false))
            .expect("run succeeds");
    }

    #[test]
    fn float_range_matches_go_loop() {
        let r = RangeConfig {
            start: 0.2,
            end: 1.5,
            step: 0.1,
        };
        let mut want = Vec::new();
        let mut v = r.start;
        while v <= r.end {
            want.push(v);
            v += r.step;
        }
        assert_eq!(float_range(r).unwrap().collect::<Vec<_>>(), want);
        assert!(float_range(RangeConfig { step: 0.0, ..r }).is_err());
    }
}
