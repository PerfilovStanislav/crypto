//! `cargo bench` — подготовка сетки и полный перебор на синтетических котировках
//! размером с реальные (2 года 4h-свечей ≈ 4380).

use std::sync::atomic::AtomicBool;

use criterion::{criterion_group, criterion_main, Criterion};
use crypto_back::analyzer::{Analyzer, TaskResult};
use crypto_back::config::{AnalyzerConfig, IndicatorConfig, RangeConfig};
use crypto_back::indicator::IndicatorType;
use crypto_back::source::{Quotes, SourceType};

/// Детерминированное случайное блуждание (xorshift + Box-Muller), без внешних зависимостей.
fn synthetic_quotes(n: usize) -> Quotes {
    let mut state = 0x9E37_79B9_7F4A_7C15u64;
    let mut uniform = move || {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        (state >> 11) as f64 / (1u64 << 53) as f64
    };
    let mut normal = move || {
        let (u1, u2) = (uniform().max(1e-12), uniform());
        (-2.0 * u1.ln()).sqrt() * (2.0 * std::f64::consts::PI * u2).cos()
    };

    let mut q = Quotes::default();
    let mut price = 100.0;
    for i in 0..n {
        let open = price;
        let close = open * (0.0003 + 0.012 * normal()).exp();
        q.timestamps.push(i as i64 * 4 * 3600);
        q.opens.push(open);
        q.closes.push(close);
        q.highs
            .push(open.max(close) * (1.0 + 0.01 * normal().abs()));
        q.lows.push(open.min(close) * (1.0 - 0.01 * normal().abs()));
        q.volumes.push(1000.0);
        price = close;
    }
    q
}

fn range(start: f64, end: f64, step: f64) -> RangeConfig {
    RangeConfig { start, end, step }
}

fn config() -> AnalyzerConfig {
    use SourceType::*;
    let sources = vec![L, O, C, H, LO, OC, LOC, OCH];
    let group = |sma_end: f64| {
        vec![
            IndicatorConfig {
                kind: IndicatorType::Sma,
                coefs: range(3.0, sma_end, 1.0),
                sources: sources.clone(),
            },
            IndicatorConfig {
                kind: IndicatorType::Ema,
                coefs: range(0.2, 1.0, 0.1),
                sources: sources.clone(),
            },
            IndicatorConfig {
                kind: IndicatorType::Tema,
                coefs: range(0.2, 0.8, 0.1),
                sources: sources.clone(),
            },
        ]
    };
    AnalyzerConfig {
        threads: 0,
        pair: "BENCH".into(),
        timeframe: "4h".into(),
        commission: 0.0018,
        min_closes: 50,
        min_signals: 70,
        min_recent_signals: 0,
        min_take_profits: 10,
        takeprofit: range(3.0, 9.0, 0.5),
        stoploss: range(3.0, 12.0, 1.0),
        indicators: vec![group(8.0), group(12.0)],
        ..Default::default()
    }
}

fn bench(c: &mut Criterion) {
    let az = Analyzer::with_cutoff_index(config(), synthetic_quotes(4380), 0);
    let grid = az.prepare().unwrap();
    eprintln!(
        "grid: {} params, {} tp/sl, {} pairs (+{} duplicates skipped), {} evaluations",
        grid.params.len(),
        grid.tp_sl.len(),
        grid.compares.len(),
        grid.duplicates.len(),
        grid.compares.len() * grid.tp_sl.len()
    );

    let mut g = c.benchmark_group("analyzer");
    g.sample_size(10);
    g.bench_function("prepare", |b| b.iter(|| az.prepare().unwrap()));
    g.bench_function("search", |b| {
        let stop = AtomicBool::new(false);
        b.iter(|| az.search(&grid, &stop, &|_: &TaskResult| {}, &|_: usize, _: usize| {}))
    });
    g.finish();
}

criterion_group!(benches, bench);
criterion_main!(benches);
