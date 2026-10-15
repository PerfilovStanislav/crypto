//! Сверка `/api/get_quotes` двух серверов (например, Go и Rust) на одинаковых запросах:
//!   cargo run --release --example api_parity -- http://localhost:8890 http://localhost:8891

use anyhow::{bail, Result};
use crypto_back::api;
use prost::Message;

/// (тип, coef, источник) — номера из `core/api/quotes.proto`.
type Ind = (i32, f64, i32);

fn request(tp: f64, sl: f64, ind1: Ind, ind2: Ind) -> api::QuotesRequest {
    let ind = |(r#type, coef, source): Ind| {
        Some(api::Indicator {
            r#type,
            coef,
            source,
        })
    };
    api::QuotesRequest {
        tf: 6, // TIMEFRAME_4H
        takeprofit: tp,
        stoploss: sl,
        ind1: ind(ind1),
        ind2: ind(ind2),
    }
}

async fn post(http: &reqwest::Client, base: &str, body: Vec<u8>) -> Result<(u16, Vec<u8>)> {
    let resp = http
        .post(format!("{base}/api/get_quotes"))
        .header("Origin", "http://localhost")
        .body(body)
        .send()
        .await?;
    Ok((resp.status().as_u16(), resp.bytes().await?.to_vec()))
}

fn rel_diff(a: &[f64], b: &[f64]) -> Option<f64> {
    (a.len() == b.len()).then(|| {
        a.iter()
            .zip(b)
            .filter(|(x, y)| x != y)
            .map(|(x, y)| (x - y).abs() / x.abs().max(y.abs()))
            .fold(0.0, f64::max)
    })
}

fn compare(a: &api::QuotesResponse, b: &api::QuotesResponse) -> Vec<String> {
    let mut diffs = Vec::new();
    let prices = |p: &Option<api::Prices>| p.as_ref().map(|p| p.price.clone()).unwrap_or_default();
    if a.time != b.time {
        diffs.push("time".into());
    }
    let (ca, cb) = (
        a.candles.clone().unwrap_or_default(),
        b.candles.clone().unwrap_or_default(),
    );
    for (name, x, y) in [
        ("L", &ca.l, &cb.l),
        ("O", &ca.o, &cb.o),
        ("C", &ca.c, &cb.c),
        ("H", &ca.h, &cb.h),
    ] {
        if prices(x) != prices(y) {
            diffs.push(format!("candles.{name}"));
        }
    }
    for (name, x, y) in [
        ("indicator1", &a.indicator1, &b.indicator1),
        ("indicator2", &a.indicator2, &b.indicator2),
    ] {
        match rel_diff(&prices(x), &prices(y)) {
            Some(d) if d <= 1e-9 => {
                if d > 0.0 {
                    println!("    {name}: rounding diff up to {d:e}");
                }
            }
            Some(d) => diffs.push(format!("{name} (rel diff {d:e})")),
            None => diffs.push(format!("{name} length")),
        }
    }
    if a.deals != b.deals {
        diffs.push(format!("deals ({} vs {})", a.deals.len(), b.deals.len()));
    }
    if (a.symbol.as_str(), a.wins, a.losses) != (b.symbol.as_str(), b.wins, b.losses) {
        diffs.push("symbol/wins/losses".into());
    }
    if rel_diff(
        &[a.profit_pct, a.max_drawdown_pct],
        &[b.profit_pct, b.max_drawdown_pct],
    )
    .unwrap()
        > 1e-12
    {
        diffs.push(format!(
            "profit/dd ({}/{} vs {}/{})",
            a.profit_pct, a.max_drawdown_pct, b.profit_pct, b.max_drawdown_pct
        ));
    }
    diffs
}

#[tokio::main]
async fn main() -> Result<()> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let [a, b] = args.as_slice() else {
        bail!("usage: api_parity <base url A> <base url B>");
    };
    let http = reqwest::Client::new();

    let cases = [
        (
            "tema H 0.2 / dema OCH 0.4",
            request(1050.0, 550.0, (4, 0.2, 4), (3, 0.4, 14)),
        ),
        (
            "sma C 7 / ema C 0.5",
            request(1200.0, 800.0, (1, 7.0, 3), (2, 0.5, 3)),
        ),
        (
            "temaZero LOC 0.6 / sma OH 12",
            request(900.0, 1500.0, (5, 0.6, 11), (1, 12.0, 9)),
        ),
        (
            "unspecified enums",
            request(1000.0, 1000.0, (0, 0.3, 0), (0, 0.6, 0)),
        ),
    ];

    let mut failed = false;
    for (name, req) in cases {
        let body = req.encode_to_vec();
        let (sa, ba) = post(&http, a, body.clone()).await?;
        let (sb, bb) = post(&http, b, body).await?;
        if sa != sb {
            println!("✗ {name}: status {sa} vs {sb}");
            failed = true;
            continue;
        }
        if ba == bb {
            println!("✓ {name}: {} bytes, identical", ba.len());
            continue;
        }
        let (ra, rb) = (
            api::QuotesResponse::decode(&*ba)?,
            api::QuotesResponse::decode(&*bb)?,
        );
        let diffs = compare(&ra, &rb);
        if diffs.is_empty() {
            println!(
                "✓ {name}: equal up to float rounding ({} deals)",
                ra.deals.len()
            );
        } else {
            println!("✗ {name}: {}", diffs.join(", "));
            failed = true;
        }
    }

    let (sa, _) = post(&http, a, b"\xff\xff garbage".to_vec()).await?;
    let (sb, _) = post(&http, b, b"\xff\xff garbage".to_vec()).await?;
    println!(
        "{} invalid body: {sa} vs {sb}",
        if sa == sb { "✓" } else { "✗" }
    );
    failed |= sa != sb;

    for method in [reqwest::Method::OPTIONS, reqwest::Method::GET] {
        let mut seen = Vec::new();
        for base in [a, b] {
            let resp = http
                .request(method.clone(), format!("{base}/api/get_quotes"))
                .header("Origin", "http://example.test")
                .send()
                .await?;
            let h = |k: &str| {
                resp.headers()
                    .get(k)
                    .and_then(|v| v.to_str().ok())
                    .unwrap_or("-")
                    .to_owned()
            };
            seen.push((
                resp.status().as_u16(),
                h("access-control-allow-origin"),
                h("access-control-allow-methods"),
                h("access-control-allow-headers"),
                h("access-control-expose-headers"),
            ));
        }
        let ok = seen[0] == seen[1];
        failed |= !ok;
        println!(
            "{} {method}: {:?}{}",
            if ok { "✓" } else { "✗" },
            seen[0],
            if ok {
                String::new()
            } else {
                format!(" vs {:?}", seen[1])
            }
        );
    }

    if failed {
        bail!("responses differ");
    }
    Ok(())
}
