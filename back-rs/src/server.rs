//! HTTP API для фронта: `POST /api/get_quotes` (protobuf из `core/api/quotes.proto`).

use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use axum::body::Bytes;
use axum::extract::{Request, State};
use axum::http::{header, HeaderMap, HeaderValue, Method, StatusCode};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use prost::Message;
use tokio_util::sync::CancellationToken;
use tower_http::catch_panic::CatchPanicLayer;
use tower_http::timeout::TimeoutLayer;
use tracing::{error, info, Instrument};

use crate::analyzer::{calculate_closings, Analyzer, TpSlParam};
use crate::api;
use crate::indicator::IndicatorType;
use crate::source::SourceType;

pub async fn serve(az: Arc<Analyzer>, port: u16, shutdown: CancellationToken) -> Result<()> {
    let listener = tokio::net::TcpListener::bind(("0.0.0.0", port))
        .await
        .with_context(|| format!("http server error: bind :{port}"))?;
    info!(port, "http server started");
    axum::serve(listener, router(az))
        .with_graceful_shutdown(shutdown.cancelled_owned())
        .await
        .context("http server error")
}

pub fn router(az: Arc<Analyzer>) -> Router {
    Router::new()
        .route("/api/get_quotes", any(get_quotes))
        .route_layer(middleware::from_fn(tracing_ids))
        .route_layer(middleware::from_fn(cors))
        .layer(CatchPanicLayer::new())
        .layer(TimeoutLayer::with_status_code(
            StatusCode::REQUEST_TIMEOUT,
            Duration::from_secs(30),
        ))
        .with_state(az)
}

async fn cors(req: Request, next: Next) -> Response {
    let origin = req
        .headers()
        .get(header::ORIGIN)
        .cloned()
        .unwrap_or(HeaderValue::from_static(""));

    let mut resp = if req.method() == Method::OPTIONS {
        StatusCode::NO_CONTENT.into_response()
    } else {
        next.run(req).await
    };

    let h = resp.headers_mut();
    h.insert(header::ACCESS_CONTROL_ALLOW_ORIGIN, origin);
    h.insert(
        header::ACCESS_CONTROL_ALLOW_METHODS,
        HeaderValue::from_static("GET, POST, OPTIONS"),
    );
    h.insert(
        header::ACCESS_CONTROL_ALLOW_HEADERS,
        HeaderValue::from_static("Content-Type, Authorization"),
    );
    h.insert(
        header::ACCESS_CONTROL_EXPOSE_HEADERS,
        HeaderValue::from_static("Content-Length, Authorization"),
    );
    resp
}

/// Берёт trace/span id из W3C `traceparent` или генерирует новые; все логи запроса идут с ними.
async fn tracing_ids(req: Request, next: Next) -> Response {
    let (trace_id, span_id) = trace_ids(req.headers());
    let span = tracing::info_span!("request", trace_id = %trace_id, span_id = %span_id);
    next.run(req).instrument(span).await
}

fn trace_ids(headers: &HeaderMap) -> (String, String) {
    let mut trace_id = String::new();
    let mut span_id = String::new();

    if let Some(tp) = headers.get("traceparent").and_then(|v| v.to_str().ok()) {
        let parts: Vec<&str> = tp.split('-').collect();
        if parts.len() == 4 {
            trace_id = parts[1].to_owned();
            span_id = parts[2].to_owned();
        }
    }

    if trace_id.is_empty() {
        let secs = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| d.as_secs() as u32);
        let mut b = [0u8; 16];
        b[..4].copy_from_slice(&secs.to_be_bytes());
        b[4..].copy_from_slice(&rand::random::<[u8; 12]>());
        trace_id = hex::encode(b);
    }
    if span_id.is_empty() {
        span_id = hex::encode(rand::random::<[u8; 8]>());
    }
    (trace_id, span_id)
}

async fn get_quotes(State(az): State<Arc<Analyzer>>, method: Method, body: Bytes) -> Response {
    if method != Method::POST {
        error!(error = %format!("method {method} not allowed"), "bad request");
        return (StatusCode::METHOD_NOT_ALLOWED, "Method not allowed\n").into_response();
    }

    let req = match api::QuotesRequest::decode(body) {
        Ok(req) => req,
        Err(e) => {
            error!(error = %e, "bad request");
            return (StatusCode::BAD_REQUEST, "Bad request\n").into_response();
        }
    };

    let resp = match tokio::task::spawn_blocking(move || quotes_response(&az, &req)).await {
        Ok(resp) => resp,
        Err(e) => {
            error!(error = %e, "bad request");
            return (StatusCode::BAD_REQUEST, "Bad request\n").into_response();
        }
    };

    (
        [(header::CONTENT_TYPE, "application/x-protobuf")],
        resp.encode_to_vec(),
    )
        .into_response()
}

/// Бэктест одной стратегии: котировки, оба индикатора и список сделок.
pub fn quotes_response(az: &Analyzer, req: &api::QuotesRequest) -> api::QuotesResponse {
    let (mut tp, mut sl) = (req.takeprofit, req.stoploss);
    if az.cfg.decimals > 0 {
        let factor = 10f64.powi(az.cfg.decimals);
        tp /= factor;
        sl /= factor;
    }
    let closes = calculate_closings(&az.quotes, TpSlParam { tp, sl }, az.cfg.commission);

    let ind1 = indicator_prices(az, req.ind1.as_ref());
    let ind2 = indicator_prices(az, req.ind2.as_ref());
    let signals = az.compare_indicators(&ind1, &ind2).unwrap_or_default();

    let mut result = 1.0;
    let mut closing_index = 0u32;
    let mut deals = Vec::new();
    let (mut wins, mut losses) = (0, 0);
    let mut max_drawdown = 0.0;

    for &open in &signals {
        if open < closing_index {
            continue;
        }
        let next = open as usize + 1;
        let trade_coef = closes.coefs[next];
        closing_index = closes.indexes[next];
        let drawdown = closes.drawdowns[next];

        if trade_coef > 1.0 {
            wins += 1;
        } else if trade_coef < 1.0 {
            losses += 1;
        }
        if drawdown > max_drawdown {
            max_drawdown = drawdown;
        }

        result *= trade_coef;
        deals.push(api::Deal {
            open: open as i32 + 1,
            close: closing_index as i32,
        });
    }

    let q = &az.quotes;
    let prices = |v: &[f64]| Some(api::Prices { price: v.to_vec() });
    api::QuotesResponse {
        time: q
            .timestamps
            .iter()
            .map(|&seconds| prost_types::Timestamp { seconds, nanos: 0 })
            .collect(),
        candles: Some(api::Candles {
            l: prices(&q.lows),
            o: prices(&q.opens),
            c: prices(&q.closes),
            h: prices(&q.highs),
        }),
        indicator1: Some(api::Prices { price: ind1 }),
        indicator2: Some(api::Prices { price: ind2 }),
        deals,
        symbol: az.cfg.pair.clone(),
        profit_pct: (result - 1.0) * 100.0,
        max_drawdown_pct: max_drawdown * 100.0,
        wins,
        losses,
    }
}

fn indicator_prices(az: &Analyzer, ind: Option<&api::Indicator>) -> Vec<f64> {
    let ind = ind.cloned().unwrap_or_default();
    az.indicator_prices(
        indicator_type(ind.r#type),
        source_type(ind.source),
        ind.coef,
    )
}

/// Номера из `IndicatorTypeEnum`; неизвестное значение — EMA, как в Go.
fn indicator_type(v: i32) -> IndicatorType {
    match v {
        1 => IndicatorType::Sma,
        3 => IndicatorType::Dema,
        4 => IndicatorType::Tema,
        5 => IndicatorType::TemaZero,
        _ => IndicatorType::Ema,
    }
}

/// Номера из `SourceTypeEnum`; неизвестное значение — L, как в Go.
fn source_type(v: i32) -> SourceType {
    use SourceType::*;
    match v {
        2 => O,
        3 => C,
        4 => H,
        5 => LO,
        6 => LC,
        7 => LH,
        8 => OC,
        9 => OH,
        10 => CH,
        11 => LOC,
        12 => LOH,
        13 => LCH,
        14 => OCH,
        _ => L,
    }
}
