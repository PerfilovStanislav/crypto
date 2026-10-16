//! Перебор стратегий «индикатор1 × индикатор2 × TP/SL» (порт `back/src/analyzer`).
//!
//! Сигнал на свече `i` — `ind2[i] > ind1[i]`. Вход по открытию свечи `i + 1`,
//! если предыдущая сделка уже закрыта; выход — по TP/SL из [`TpSlClose`].

mod closings;
mod fast;
mod format;
#[cfg(feature = "gpu")]
pub mod gpu;
mod refine;

use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::Mutex;

use anyhow::{bail, Result};
use chrono::{Months, Utc};
use rayon::prelude::*;

pub use closings::{calculate_closings, has_enough_closes, TpSlClose, TpSlParam};
pub use fast::{
    evaluate_trades, fill_signal_bits, scout, scout_interleaved, signal_weights, signal_words,
    ColdOutcome, HotOutcome, Outcomes,
};
pub use refine::{Refined, Refinement, Span, MAX_REFINE_ROUNDS};

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
    pub tp_sl: Vec<(TpSlParam, Outcomes)>,
    /// Пары для перебора: по одной на каждый уникальный набор сигналов.
    pub compares: Vec<Compare>,
    /// Пропущенные пары `(дубликат, представитель из compares)` с тем же набором сигналов:
    /// на любом TP/SL они дают ровно тот же результат.
    pub duplicates: Vec<(Compare, Compare)>,
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

    /// Сетка из конфига: индикаторы и TP/SL с теми же шагами, что в Go.
    pub fn prepare(&self) -> Result<Grid> {
        let mut groups = Vec::with_capacity(self.cfg.indicators.len());
        for group in &self.cfg.indicators {
            let mut params = Vec::new();
            for ind in group {
                for &source in &ind.sources {
                    for coef in float_range(ind.coefs)? {
                        params.push(IndicatorParams {
                            kind: ind.kind,
                            source,
                            coef,
                        });
                    }
                }
            }
            groups.push(params);
        }

        let tps: Vec<f64> = float_range(self.cfg.takeprofit)?.collect();
        let sls: Vec<f64> = float_range(self.cfg.stoploss)?.collect();
        // Как в Go: по росту SL идём, пока набирается minCloses тейк-профитов.
        let tp_sl: Vec<TpSlParam> = tps
            .par_iter()
            .flat_map_iter(|&tp| {
                sls.iter()
                    .map(move |&sl| TpSlParam { tp, sl })
                    .take_while(|&p| has_enough_closes(&self.quotes, p, self.cfg.min_closes))
            })
            .collect();

        Ok(self.prepare_grid(&groups, tp_sl))
    }

    /// Сетка из явных списков: группы индикаторов (пары строятся между соседними группами)
    /// и TP/SL.
    pub fn prepare_grid(&self, groups: &[Vec<IndicatorParams>], tp_sl: Vec<TpSlParam>) -> Grid {
        let mut grid = Grid {
            weights: signal_weights(self.ln),
            ..Grid::default()
        };
        self.fill_indicator_params(&mut grid, groups);
        self.fill_tp_sl(&mut grid, tp_sl);
        self.fill_compares(&mut grid);
        grid
    }

    fn fill_indicator_params(&self, grid: &mut Grid, groups: &[Vec<IndicatorParams>]) {
        // Go-версия держала map[IndicatorParams]; ключ с f64 заменён на биты coef.
        let mut index: HashMap<(IndicatorType, SourceType, u64), usize> = HashMap::new();
        for group in groups {
            let ids = group
                .iter()
                .map(|p| {
                    *index
                        .entry((p.kind, p.source, p.coef.to_bits()))
                        .or_insert_with(|| {
                            grid.params.push(*p);
                            grid.params.len() - 1
                        })
                })
                .collect();
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
    }

    fn fill_tp_sl(&self, grid: &mut Grid, params: Vec<TpSlParam>) {
        grid.tp_sl = params
            .into_par_iter()
            .map(|p| {
                let closes = calculate_closings(&self.quotes, p, self.cfg.commission);
                (p, Outcomes::from_closings(&closes))
            })
            .collect();
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
        let passing: Vec<Compare> = pairs
            .into_par_iter()
            .filter(|&(a, b)| self.enough_signals(&prices[a], &prices[b]))
            .map(|(ind1, ind2)| Compare { ind1, ind2 })
            .collect();

        let (compares, duplicates) = self.dedupe_by_signals(passing, prices);
        grid.compares = compares;
        grid.duplicates = duplicates;
    }

    /// Оставляет первую пару из каждой группы с одинаковым набором сигналов. Группы ищутся
    /// по хешу битов, совпадение хешей перепроверяется побитово — коллизия ничего не сломает.
    fn dedupe_by_signals(
        &self,
        pairs: Vec<Compare>,
        prices: &[Vec<f64>],
    ) -> (Vec<Compare>, Vec<(Compare, Compare)>) {
        let words = signal_words(self.ln);
        let bits_of = |c: &Compare, buf: &mut Vec<u64>| {
            buf.resize(words, 0);
            fill_signal_bits(buf, &prices[c.ind1], &prices[c.ind2]);
        };

        let hashes: Vec<u64> = pairs
            .par_iter()
            .map_init(Vec::new, |buf, c| {
                bits_of(c, buf);
                buf.iter().fold(0u64, |h, &w| {
                    (h.rotate_left(5) ^ w).wrapping_mul(0x517c_c1b7_2722_0a95)
                })
            })
            .collect();

        // Порядок внутри группы — исходный, поэтому представитель — самая ранняя пара.
        let mut order: Vec<usize> = (0..pairs.len()).collect();
        order.par_sort_unstable_by_key(|&i| (hashes[i], i));
        let groups: Vec<&[usize]> = order
            .chunk_by(|&a, &b| hashes[a] == hashes[b])
            .filter(|g| g.len() > 1)
            .collect();

        // (индекс дубликата, индекс представителя)
        let dup_of: Vec<(usize, usize)> = groups
            .par_iter()
            .flat_map_iter(|group| {
                let (mut a, mut b) = (Vec::new(), Vec::new());
                let mut reps: Vec<usize> = Vec::new();
                let mut found = Vec::new();
                for &i in group.iter() {
                    bits_of(&pairs[i], &mut a);
                    let same = reps.iter().copied().find(|&r| {
                        bits_of(&pairs[r], &mut b);
                        a == b
                    });
                    match same {
                        Some(r) => found.push((i, r)),
                        None => reps.push(i),
                    }
                }
                found
            })
            .collect();

        let mut keep = vec![true; pairs.len()];
        let duplicates = dup_of
            .iter()
            .map(|&(i, r)| {
                keep[i] = false;
                (pairs[i], pairs[r])
            })
            .collect();
        let compares = pairs
            .into_iter()
            .zip(keep)
            .filter_map(|(c, k)| k.then_some(c))
            .collect();
        (compares, duplicates)
    }

    /// Перебирает все пары × TP/SL на текущем пуле rayon. `on_best` вызывается на каждый
    /// новый глобальный максимум `weighted_profit_to_dd`, `on_progress(done, total)` — по
    /// мере обработки пар. Возвращает лучший результат.
    ///
    /// Пары идут блоками по [`PAIRS_PER_BLOCK`]: маски сигналов блока (~35 КБ) и горячие
    /// исходы одного TP/SL (~70 КБ) вместе помещаются в L1.
    pub fn search(
        &self,
        grid: &Grid,
        stop: &AtomicBool,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_progress: &(dyn Fn(usize, usize) + Sync),
    ) -> Option<TaskResult> {
        self.search_above(grid, stop, 0.0, on_best, on_progress)
    }

    /// [`Analyzer::search`], но учитываются только результаты с `weighted_profit_to_dd`
    /// строго больше `floor`; `None` — таких нет.
    pub fn search_above(
        &self,
        grid: &Grid,
        stop: &AtomicBool,
        floor: f64,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_progress: &(dyn Fn(usize, usize) + Sync),
    ) -> Option<TaskResult> {
        let tracker = Tracker::new(self, grid, on_best, floor);
        let completed = AtomicUsize::new(0);
        let total = grid.compares.len();

        grid.compares.par_chunks(PAIRS_PER_BLOCK).for_each_init(
            Vec::new,
            |signals: &mut Vec<u64>, block| {
                if stop.load(Ordering::Relaxed) {
                    return;
                }
                self.scout_block(&tracker, block, signals);
                let done = completed.fetch_add(block.len(), Ordering::Relaxed) + block.len();
                on_progress(done, total);
            },
        );

        tracker.into_best()
    }

    /// То же, что [`Analyzer::search`], но быстрый проход делает GPU: CPU считает маски
    /// сигналов партии и точно пересчитывает найденных GPU кандидатов.
    #[cfg(feature = "gpu")]
    pub fn search_gpu(
        &self,
        grid: &Grid,
        stop: &AtomicBool,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_progress: &(dyn Fn(usize, usize) + Sync),
    ) -> Result<Option<TaskResult>> {
        let words = signal_words(self.ln);
        let gpu = gpu::GpuScout::new(grid, self.ln, words)?;
        tracing::info!(adapter = %gpu.adapter, "GPU scout ready");
        println!("GPU: {}", gpu.adapter);

        let tracker = Tracker::new(self, grid, on_best, 0.0);
        let total = grid.compares.len();
        let mut done = 0;
        let mut bits: Vec<u64> = Vec::new();
        let (mut t_bits, mut t_gpu, mut t_exact) = Default::default();
        let mut candidates = 0usize;

        for batch in grid.compares.chunks(gpu::PAIRS_PER_BATCH) {
            if stop.load(Ordering::Relaxed) {
                break;
            }
            let started = std::time::Instant::now();
            bits.resize(batch.len() * words, 0);
            bits.par_chunks_mut(words)
                .zip(batch.par_iter())
                .for_each(|(b, cmp)| {
                    fill_signal_bits(b, &grid.prices[cmp.ind1], &grid.prices[cmp.ind2])
                });

            let threshold = tracker.max().max(3.0);
            let scouted = std::time::Instant::now();
            t_bits += scouted - started;
            let found = gpu.candidates(&bits, batch.len(), threshold, self.cfg.min_take_profits)?;
            let exact = std::time::Instant::now();
            t_gpu += exact - scouted;
            candidates += found.as_ref().map_or(0, Vec::len);
            match found {
                Some(found) => found.par_iter().for_each(|&(p, t)| {
                    let p = p as usize;
                    tracker.consider(
                        &batch[p],
                        t as usize,
                        &bits[p * words..][..words],
                        f64::INFINITY,
                    );
                }),
                None => {
                    tracing::warn!(
                        pairs = batch.len(),
                        "GPU candidate buffer overflow, batch searched on CPU"
                    );
                    batch
                        .par_chunks(PAIRS_PER_BLOCK)
                        .for_each_init(Vec::new, |signals, block| {
                            self.scout_block(&tracker, block, signals)
                        });
                }
            }
            t_exact += exact.elapsed();
            done += batch.len();
            on_progress(done, total);
        }

        let (t_bits, t_gpu, t_exact): (
            std::time::Duration,
            std::time::Duration,
            std::time::Duration,
        ) = (t_bits, t_gpu, t_exact);
        tracing::info!(
            masks = ?t_bits,
            gpu = ?t_gpu,
            exact = ?t_exact,
            candidates,
            "GPU search finished"
        );
        Ok(tracker.into_best())
    }

    /// Быстрый проход блока пар по всем TP/SL на CPU, кандидаты — в точный пересчёт.
    fn scout_block(&self, tracker: &Tracker, block: &[Compare], signals: &mut Vec<u64>) {
        let grid = tracker.grid;
        let words = signal_words(self.ln);
        let min_tp = self.cfg.min_take_profits;

        signals.resize(block.len() * words, 0);
        for (cmp, bits) in block.iter().zip(signals.chunks_exact_mut(words)) {
            fill_signal_bits(bits, &grid.prices[cmp.ind1], &grid.prices[cmp.ind2]);
        }

        for (t, (_, outcomes)) in grid.tp_sl.iter().enumerate() {
            let lanes = block
                .chunks_exact(LANES)
                .zip(signals.chunks_exact(LANES * words));
            for (cmps, chunk) in lanes {
                let bits: [&[u64]; LANES] = std::array::from_fn(|i| &chunk[i * words..][..words]);
                let bounds = scout_interleaved(bits, &outcomes.hot, &grid.weights, min_tp);
                for ((cmp, bits), bound) in cmps.iter().zip(bits).zip(bounds) {
                    if let Some(bound) = bound {
                        tracker.consider(cmp, t, bits, bound);
                    }
                }
            }
            let tail = block.len() / LANES * LANES;
            for (cmp, bits) in block[tail..]
                .iter()
                .zip(signals[tail * words..].chunks_exact(words))
            {
                if let Some(bound) = scout(bits, &outcomes.hot, &grid.weights, min_tp) {
                    tracker.consider(cmp, t, bits, bound);
                }
            }
        }
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
                duplicate_pairs = grid.duplicates.len(),
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

            let on_best = |r: &TaskResult| println!("{r}");
            let best = self.search_main(&grid, stop, &on_best, &progress)?;
            drop(grid);

            let Some(best) = best else {
                return Ok(None);
            };
            if stop.load(Ordering::Relaxed) {
                return Ok(Some(best));
            }

            // Тот же перебор на мелкой сетке вокруг лучшей стратегии (всегда на CPU: сетка мала),
            // пока лучшее значение оказывается на краю сетки.
            let on_round = |round: usize, r: &Refinement, grid: &Grid, edges: &[&str]| {
                if !edges.is_empty() {
                    println!(
                        "best is at the edge of the grid ({}), refining again",
                        edges.join(", ")
                    );
                }
                println!("Refine #{round}: {r}");
                println!("Jobs:{}", grid.compares.len() * grid.tp_sl.len());
                println!("{}", format::header());
            };
            let refined = self.refine(best, stop, &on_best, &on_round)?;
            if !refined.edges.is_empty() {
                println!(
                    "best is still at the edge of the grid ({}) after {} refinement rounds",
                    refined.edges.join(", "),
                    refined.rounds
                );
            }
            Ok(Some(refined.best))
        })
    }

    /// Основной перебор: на GPU, если он включён и доступен, иначе на CPU.
    fn search_main(
        &self,
        grid: &Grid,
        stop: &AtomicBool,
        on_best: &(dyn Fn(&TaskResult) + Sync),
        on_progress: &(dyn Fn(usize, usize) + Sync),
    ) -> Result<Option<TaskResult>> {
        if self.cfg.gpu {
            #[cfg(feature = "gpu")]
            match self.search_gpu(grid, stop, on_best, on_progress) {
                Ok(best) => return Ok(best),
                Err(e) => {
                    tracing::warn!(error = %format!("{e:#}"), "GPU unavailable, searching on CPU");
                    println!("GPU unavailable ({e:#}), searching on CPU");
                }
            }
            #[cfg(not(feature = "gpu"))]
            bail!("analyzer.gpu requires a build with `--features gpu`");
        }
        Ok(self.search(grid, stop, on_best, on_progress))
    }
}

/// Сколько пар обрабатывается вместе на одном TP/SL (см. [`Analyzer::search`]).
pub const PAIRS_PER_BLOCK: usize = 64;

/// Сколько пар оценивается вперемешку, см. [`evaluate_trades_interleaved`].
const LANES: usize = 4;

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

/// Глобальный максимум `weighted_profit_to_dd` и лучший результат во время перебора.
struct Tracker<'a> {
    analyzer: &'a Analyzer,
    grid: &'a Grid,
    on_best: &'a (dyn Fn(&TaskResult) + Sync),
    max_bits: AtomicU64,
    best: Mutex<Option<TaskResult>>,
}

impl<'a> Tracker<'a> {
    fn new(
        analyzer: &'a Analyzer,
        grid: &'a Grid,
        on_best: &'a (dyn Fn(&TaskResult) + Sync),
        floor: f64,
    ) -> Self {
        Self {
            analyzer,
            grid,
            on_best,
            max_bits: AtomicU64::new(floor.to_bits()),
            best: Mutex::new(None),
        }
    }

    fn max(&self) -> f64 {
        f64::from_bits(self.max_bits.load(Ordering::Relaxed))
    }

    /// Точный пересчёт пары на TP/SL `t`, если верхняя оценка `bound` может побить максимум.
    fn consider(&self, cmp: &Compare, t: usize, bits: &[u64], bound: f64) {
        if bound <= self.max() {
            return;
        }
        let (param, outcomes) = &self.grid.tp_sl[t];
        let min_tp = self.analyzer.cfg.min_take_profits;
        if let Some(metrics) = evaluate_trades(bits, outcomes, &self.grid.weights, min_tp) {
            self.report(cmp, param, metrics);
        }
    }

    fn report(&self, cmp: &Compare, param: &TpSlParam, metrics: Metrics) {
        if metrics.weighted_profit_to_dd <= self.max() {
            return;
        }
        // Максимум меняется и печатается только под замком: иначе параллельные находки
        // печатались бы не по порядку и последняя строка могла оказаться не лучшей.
        let mut best = self.best.lock().unwrap_or_else(|e| e.into_inner());
        if metrics.weighted_profit_to_dd <= self.max() {
            return;
        }
        self.max_bits
            .store(metrics.weighted_profit_to_dd.to_bits(), Ordering::Relaxed);

        let cfg = &self.analyzer.cfg;
        let result = TaskResult {
            task: Task {
                ind1: self.grid.params[cmp.ind1],
                ind2: self.grid.params[cmp.ind2],
                tp_sl: *param,
                timeframe: cfg.timeframe.clone(),
                decimals: cfg.decimals,
            },
            metrics,
        };
        (self.on_best)(&result);
        *best = Some(result);
    }

    fn into_best(self) -> Option<TaskResult> {
        self.best.into_inner().unwrap_or_else(|e| e.into_inner())
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
    fn identical_signal_sets_are_searched_once() {
        let one = RangeConfig {
            start: 1.0,
            end: 1.0,
            step: 1.0,
        };
        let ind = |kind, coef| IndicatorConfig {
            kind,
            coefs: coef,
            sources: vec![SourceType::C],
        };
        // ema(1.0) и dema(1.0) равны самим ценам, значит пары с ними дают одни и те же сигналы.
        let cfg = AnalyzerConfig {
            min_signals: 1,
            takeprofit: one,
            stoploss: one,
            indicators: vec![
                vec![ind(IndicatorType::Ema, one), ind(IndicatorType::Dema, one)],
                vec![ind(
                    IndicatorType::Sma,
                    RangeConfig {
                        start: 5.0,
                        end: 5.0,
                        step: 1.0,
                    },
                )],
            ],
            ..Default::default()
        };
        let n = 200;
        let closes: Vec<f64> = (0..n)
            .map(|i| 100.0 + (i as f64 * 0.3).sin() * 5.0)
            .collect();
        let quotes = Quotes {
            timestamps: (0..n as i64).collect(),
            opens: closes.clone(),
            highs: closes.iter().map(|c| c + 1.0).collect(),
            lows: closes.iter().map(|c| c - 1.0).collect(),
            volumes: vec![1.0; n],
            closes,
        };

        let grid = Analyzer::with_cutoff_index(cfg, quotes, 0)
            .prepare()
            .unwrap();
        assert_eq!(grid.compares.len(), 1);
        assert_eq!(grid.duplicates.len(), 1);
        let (dup, rep) = grid.duplicates[0];
        assert_eq!(rep, grid.compares[0]);
        assert_eq!(
            grid.params[rep.ind1].kind,
            IndicatorType::Ema,
            "earliest pair is kept"
        );
        assert_eq!(grid.params[dup.ind1].kind, IndicatorType::Dema);
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
