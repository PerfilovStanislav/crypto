//! Сверка с Go-версией на фикстурах из `back/src/analyzer/golden_test.go`.
//! Сгенерировать (из back/src):
//!   GOLDEN_OUT=$PWD/../../back-rs/tests/fixtures/golden.json go test ./analyzer -run TestGolden

use std::collections::{HashMap, HashSet};
use std::path::Path;

use std::sync::atomic::AtomicBool;

use crypto_back::analyzer::{
    calculate_closings, evaluate, evaluate_trades, fill_signal_bits, scout, scout_interleaved,
    signal_words, Analyzer, Compare, Grid, Metrics, TaskResult,
};
use crypto_back::config::AnalyzerConfig;
use crypto_back::indicator::IndicatorType;
use crypto_back::source::{Quotes, SourceType};
use serde::{Deserialize, Deserializer};

const FIXTURE: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures/golden.json");

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Golden {
    config: AnalyzerConfig,
    cutoff_index: usize,
    quotes: GoldenQuotes,
    indicators: Vec<GoldenIndicator>,
    tp_sl: Vec<GoldenTpSl>,
    compares: Vec<GoldenCompare>,
    passed: Vec<GoldenEval>,
}

#[derive(Deserialize)]
struct GoldenQuotes {
    timestamps: Vec<i64>,
    lows: Vec<f64>,
    opens: Vec<f64>,
    closes: Vec<f64>,
    highs: Vec<f64>,
    volumes: Vec<f64>,
}

#[derive(Deserialize, Clone, Copy)]
struct GoldenKey {
    #[serde(rename = "type")]
    kind: IndicatorType,
    source: SourceType,
    coef: f64,
}

type Key = (IndicatorType, SourceType, u64);

impl GoldenKey {
    fn key(self) -> Key {
        (self.kind, self.source, self.coef.to_bits())
    }
}

#[derive(Deserialize)]
struct GoldenIndicator {
    key: GoldenKey,
    values: Vec<f64>,
}

#[derive(Deserialize)]
struct GoldenTpSl {
    tp: f64,
    sl: f64,
    indexes: Vec<u32>,
    coefs: Vec<f64>,
    drawdowns: Vec<f64>,
}

#[derive(Deserialize)]
struct GoldenCompare {
    ind1: GoldenKey,
    ind2: GoldenKey,
    signals: Vec<u32>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct GoldenEval {
    compare: usize,
    tp_sl: usize,
    coef: f64,
    max_drawdown: f64,
    #[serde(deserialize_with = "go_float")]
    profit_to_dd: f64,
    profit_to_candles: f64,
    wins: usize,
    losses: usize,
}

/// Go пишет ±Inf/NaN строкой (encoding/json их не поддерживает).
fn go_float<'de, D: Deserializer<'de>>(d: D) -> Result<f64, D::Error> {
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum F {
        Num(f64),
        Str(String),
    }
    Ok(match F::deserialize(d)? {
        F::Num(v) => v,
        F::Str(s) => match s.as_str() {
            "+Inf" => f64::INFINITY,
            "-Inf" => f64::NEG_INFINITY,
            _ => f64::NAN,
        },
    })
}

fn rel_eq(a: f64, b: f64, tol: f64) -> bool {
    a == b || (a - b).abs() <= tol * a.abs().max(b.abs())
}

struct Fixture {
    golden: Golden,
    az: Analyzer,
    grid: Grid,
}

fn load() -> Option<Fixture> {
    if !Path::new(FIXTURE).exists() {
        eprintln!("skip: {FIXTURE} not found, see header of tests/golden.rs");
        return None;
    }
    let golden: Golden = serde_json::from_slice(&std::fs::read(FIXTURE).unwrap()).unwrap();
    let q = &golden.quotes;
    let quotes = Quotes {
        timestamps: q.timestamps.clone(),
        lows: q.lows.clone(),
        opens: q.opens.clone(),
        closes: q.closes.clone(),
        highs: q.highs.clone(),
        volumes: q.volumes.clone(),
    };
    let az = Analyzer::with_cutoff_index(golden.config.clone(), quotes, golden.cutoff_index);
    let grid = az.prepare().unwrap();
    Some(Fixture { golden, az, grid })
}

fn param_index(grid: &Grid) -> HashMap<Key, usize> {
    grid.params
        .iter()
        .enumerate()
        .map(|(i, p)| ((p.kind, p.source, p.coef.to_bits()), i))
        .collect()
}

#[test]
fn indicators_match_go() {
    let Some(f) = load() else { return };
    let index = param_index(&f.grid);
    assert_eq!(
        index.len(),
        f.golden.indicators.len(),
        "same parameter grid"
    );

    let (mut exact, mut total, mut worst) = (0, 0, 0f64);
    for ind in &f.golden.indicators {
        let id = *index.get(&ind.key.key()).unwrap_or_else(|| {
            panic!(
                "missing {:?} {:?} {}",
                ind.key.kind, ind.key.source, ind.key.coef
            )
        });
        let ours = &f.grid.prices[id];
        assert_eq!(ours.len(), ind.values.len());
        for (a, b) in ours.iter().zip(&ind.values) {
            total += 1;
            if a == b {
                exact += 1;
            } else {
                worst = worst.max((a - b).abs() / a.abs().max(b.abs()));
            }
        }
    }
    eprintln!("indicators: {exact}/{total} bit-exact, worst relative diff {worst:e}");
    assert!(worst <= 1e-9, "worst relative diff {worst:e}");
}

#[test]
fn closings_match_go_exactly() {
    let Some(f) = load() else { return };
    assert_eq!(f.grid.tp_sl.len(), f.golden.tp_sl.len(), "same TP/SL grid");

    let ours: HashMap<(u64, u64), usize> = f
        .grid
        .tp_sl
        .iter()
        .enumerate()
        .map(|(i, (p, _))| ((p.tp.to_bits(), p.sl.to_bits()), i))
        .collect();
    for g in &f.golden.tp_sl {
        let i = ours[&(g.tp.to_bits(), g.sl.to_bits())];
        let c = &f.grid.tp_sl[i].1;
        let indexes: Vec<u32> = c.hot.iter().map(|o| o.close).collect();
        let coefs: Vec<f64> = c.cold.iter().map(|o| o.coef).collect();
        let drawdowns: Vec<f64> = c.cold.iter().map(|o| o.drawdown).collect();
        assert_eq!(indexes, g.indexes, "indexes tp={} sl={}", g.tp, g.sl);
        assert_eq!(coefs, g.coefs, "coefs tp={} sl={}", g.tp, g.sl);
        assert_eq!(drawdowns, g.drawdowns, "drawdowns tp={} sl={}", g.tp, g.sl);
    }
}

#[test]
fn compares_match_go() {
    let Some(f) = load() else { return };
    let index = param_index(&f.grid);
    // Перебираемые пары плюс отброшенные дубликаты — это ровно пары Go.
    let ours: HashSet<(usize, usize)> = f
        .grid
        .compares
        .iter()
        .chain(f.grid.duplicates.iter().map(|(dup, _)| dup))
        .map(|c| (c.ind1, c.ind2))
        .collect();
    assert_eq!(
        f.grid.compares.len() + f.grid.duplicates.len(),
        ours.len(),
        "no pair counted twice"
    );
    assert_eq!(ours.len(), f.golden.compares.len(), "same set of pairs");
    eprintln!(
        "pairs: {} searched, {} skipped as duplicates",
        f.grid.compares.len(),
        f.grid.duplicates.len()
    );

    let kept: HashSet<_> = f.grid.compares.iter().copied().collect();
    for (dup, rep) in &f.grid.duplicates {
        assert!(kept.contains(rep), "representative is searched");
        let signals = |c: &crypto_back::analyzer::Compare| {
            f.az.compare_indicators(&f.grid.prices[c.ind1], &f.grid.prices[c.ind2])
        };
        assert_eq!(signals(dup), signals(rep), "duplicate has the same signals");
    }

    for g in &f.golden.compares {
        let (a, b) = (index[&g.ind1.key()], index[&g.ind2.key()]);
        assert!(ours.contains(&(a, b)));
        let signals =
            f.az.compare_indicators(&f.grid.prices[a], &f.grid.prices[b])
                .expect("pair passes signal filters");
        assert_eq!(signals, g.signals);
    }
}

#[test]
fn evaluations_match_go() {
    let Some(f) = load() else { return };
    let index = param_index(&f.grid);
    let tp_sl: HashMap<(u64, u64), usize> = f
        .grid
        .tp_sl
        .iter()
        .enumerate()
        .map(|(i, (p, _))| ((p.tp.to_bits(), p.sl.to_bits()), i))
        .collect();
    let min_tp = f.golden.config.min_take_profits;
    let passed: HashMap<(usize, usize), &GoldenEval> = f
        .golden
        .passed
        .iter()
        .map(|e| ((e.compare, e.tp_sl), e))
        .collect();

    let mut matched = 0;
    let mut bits = vec![0; signal_words(f.az.len())];
    let mut scouted = 0;
    for (ci, gc) in f.golden.compares.iter().enumerate() {
        let (a, b) = (index[&gc.ind1.key()], index[&gc.ind2.key()]);
        let (p1, p2) = (&f.grid.prices[a], &f.grid.prices[b]);
        let signals = f.az.compare_indicators(p1, p2).unwrap();
        fill_signal_bits(&mut bits, p1, p2);

        for (ti, gt) in f.golden.tp_sl.iter().enumerate() {
            let (param, outcomes) = &f.grid.tp_sl[tp_sl[&(gt.tp.to_bits(), gt.sl.to_bits())]];
            let closes = calculate_closings(&f.az.quotes, *param, f.golden.config.commission);
            let ours = evaluate(&signals, &closes, f.az.len(), min_tp);

            // Быстрый перебор: те же решения и метрики, weighted pr/dd — с точностью до округления.
            let fast = evaluate_trades(&bits, outcomes, &f.grid.weights, min_tp);

            // Быстрый проход: отсекает только то, что точно не пройдёт, и оценивает сверху.
            let bound = scout(&bits, &outcomes.hot, &f.grid.weights, min_tp);
            match (bound, &fast) {
                (Some(b), Some(m)) => assert!(b >= m.weighted_profit_to_dd, "{b} < {m:?}"),
                (None, Some(m)) => panic!("scout dropped a passing strategy {m:?}"),
                _ => {}
            }
            scouted += bound.is_some() as usize;

            match (&fast, &ours) {
                (Some(a), Some(b)) => {
                    assert!(rel_eq(
                        a.weighted_profit_to_dd,
                        b.weighted_profit_to_dd,
                        1e-12
                    ));
                    let strip = |m: &Metrics| Metrics {
                        weighted_profit_to_dd: 0.0,
                        ..*m
                    };
                    assert_eq!(
                        strip(a),
                        strip(b),
                        "compare #{ci} tp={} sl={}",
                        gt.tp,
                        gt.sl
                    );
                }
                (None, None) => {}
                _ => panic!(
                    "fast/reference disagree: compare #{ci} tp={} sl={}",
                    gt.tp, gt.sl
                ),
            }

            match (ours, passed.get(&(ci, ti))) {
                (None, None) => {}
                (Some(m), Some(g)) => {
                    assert_metrics(&m, g);
                    matched += 1;
                }
                (ours, go) => panic!(
                    "compare #{ci} tp={} sl={}: rust passed={}, go passed={}",
                    gt.tp,
                    gt.sl,
                    ours.is_some(),
                    go.is_some()
                ),
            }
        }
    }
    eprintln!("evaluations: {matched} passing strategies match Go, scout kept {scouted}");
    assert_eq!(matched, f.golden.passed.len());
}

fn assert_metrics(m: &Metrics, g: &GoldenEval) {
    let tol = 1e-12;
    assert!(rel_eq(m.coef, g.coef, tol), "coef {} vs {}", m.coef, g.coef);
    assert!(
        rel_eq(m.max_drawdown, g.max_drawdown, tol),
        "dd {} vs {}",
        m.max_drawdown,
        g.max_drawdown
    );
    assert!(
        rel_eq(m.profit_to_dd, g.profit_to_dd, tol),
        "pr/dd {} vs {}",
        m.profit_to_dd,
        g.profit_to_dd
    );
    assert!(
        rel_eq(m.profit_to_candles, g.profit_to_candles, tol),
        "pr/cnd {} vs {}",
        m.profit_to_candles,
        g.profit_to_candles
    );
    assert_eq!((m.wins, m.losses), (g.wins, g.losses));
}

#[test]
fn interleaved_scout_matches_single_lane() {
    let Some(f) = load() else { return };
    let words = signal_words(f.az.len());
    let min_tp = f.golden.config.min_take_profits;
    let masks: Vec<Vec<u64>> = f
        .grid
        .compares
        .iter()
        .map(|c| {
            let mut bits = vec![0; words];
            fill_signal_bits(&mut bits, &f.grid.prices[c.ind1], &f.grid.prices[c.ind2]);
            bits
        })
        .collect();

    let mut checked = 0;
    for (_, outcomes) in &f.grid.tp_sl {
        for group in masks.chunks_exact(3) {
            let bits = [&group[0][..], &group[1][..], &group[2][..]];
            let interleaved = scout_interleaved(bits, &outcomes.hot, &f.grid.weights, min_tp);
            for (b, got) in bits.iter().zip(interleaved) {
                assert_eq!(got, scout(b, &outcomes.hot, &f.grid.weights, min_tp));
                checked += 1;
            }
        }
    }
    assert!(checked > 0);
}

/// `search` с отсевом и точным пересчётом находит тот же максимум, что полный точный перебор.
#[test]
fn search_finds_exact_best() {
    let Some(f) = load() else { return };
    let words = signal_words(f.az.len());
    let min_tp = f.golden.config.min_take_profits;

    let mut brute: Option<(f64, Compare, usize)> = None;
    let mut bits = vec![0; words];
    for cmp in &f.grid.compares {
        fill_signal_bits(
            &mut bits,
            &f.grid.prices[cmp.ind1],
            &f.grid.prices[cmp.ind2],
        );
        for (ti, (_, outcomes)) in f.grid.tp_sl.iter().enumerate() {
            if let Some(m) = evaluate_trades(&bits, outcomes, &f.grid.weights, min_tp) {
                if brute.is_none_or(|(w, _, _)| m.weighted_profit_to_dd > w) {
                    brute = Some((m.weighted_profit_to_dd, *cmp, ti));
                }
            }
        }
    }
    let (want, cmp, ti) = brute.expect("fixture has passing strategies");

    let found =
        f.az.search(
            &f.grid,
            &AtomicBool::new(false),
            &|_: &TaskResult| {},
            &|_, _| {},
        )
        .expect("search finds a result");
    assert_eq!(found.metrics.weighted_profit_to_dd, want);
    assert_eq!(found.task.ind1, f.grid.params[cmp.ind1]);
    assert_eq!(found.task.ind2, f.grid.params[cmp.ind2]);
    assert_eq!(found.task.tp_sl, f.grid.tp_sl[ti].0);
}

/// GPU отбирает с запасом: среди его кандидатов (порог 3) есть все стратегии, которые проходят
/// фильтры по точному расчёту, а `search_gpu` находит тот же максимум, что CPU.
#[cfg(feature = "gpu")]
#[test]
fn gpu_candidates_cover_exact_passes() {
    use crypto_back::analyzer::gpu::GpuScout;

    let Some(f) = load() else { return };
    let words = signal_words(f.az.len());
    let min_tp = f.golden.config.min_take_profits;
    let gpu = match GpuScout::new(&f.grid, f.az.len(), words) {
        Ok(gpu) => gpu,
        Err(e) => {
            eprintln!("skip: no GPU ({e:#})");
            return;
        }
    };

    let mut bits = vec![0u64; f.grid.compares.len() * words];
    for (cmp, b) in f.grid.compares.iter().zip(bits.chunks_exact_mut(words)) {
        fill_signal_bits(b, &f.grid.prices[cmp.ind1], &f.grid.prices[cmp.ind2]);
    }
    let found: HashSet<(usize, usize)> = gpu
        .candidates(&bits, f.grid.compares.len(), 3.0, min_tp)
        .unwrap()
        .expect("candidate buffer is large enough")
        .into_iter()
        .map(|(p, t)| (p as usize, t as usize))
        .collect();

    let mut passing = 0;
    for (p, b) in bits.chunks_exact(words).enumerate() {
        for (t, (_, outcomes)) in f.grid.tp_sl.iter().enumerate() {
            if evaluate_trades(b, outcomes, &f.grid.weights, min_tp).is_some() {
                passing += 1;
                assert!(found.contains(&(p, t)), "GPU missed pair #{p} tp/sl #{t}");
            }
        }
    }
    eprintln!(
        "gpu: {} candidates cover {passing} passing strategies",
        found.len()
    );

    let cpu =
        f.az.search(
            &f.grid,
            &AtomicBool::new(false),
            &|_: &TaskResult| {},
            &|_, _| {},
        )
        .unwrap();
    let on_gpu =
        f.az.search_gpu(
            &f.grid,
            &AtomicBool::new(false),
            &|_: &TaskResult| {},
            &|_, _| {},
        )
        .unwrap()
        .unwrap();
    assert_eq!(on_gpu.metrics, cpu.metrics);
    assert_eq!(
        (on_gpu.task.ind1, on_gpu.task.ind2, on_gpu.task.tp_sl),
        (cpu.task.ind1, cpu.task.ind2, cpu.task.tp_sl)
    );
}
