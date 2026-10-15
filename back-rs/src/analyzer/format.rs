//! Консольный вывод результатов в том же виде, что и `analyzer/formating.go`.

use std::fmt;

use super::{IndicatorParams, Task, TaskResult};

const WHITE: u8 = 37;
const HI_RED: u8 = 91;
const HI_GREEN: u8 = 92;
const HI_YELLOW: u8 = 93;
const HI_MAGENTA: u8 = 95;
const HI_CYAN: u8 = 96;

pub(crate) fn clr(text: &str, code: u8) -> String {
    format!("\x1b[{code}m{text}\x1b[0m")
}

pub(crate) fn header() -> String {
    format!(
        "{} {}",
        clr("     Profit pr", HI_GREEN),
        clr("  DrawDown dd", HI_RED)
    )
}

fn round2(v: f64) -> f64 {
    (v * 100.0).round() / 100.0
}

/// Аналог `%{width}.{prec}f` из Go, включая `+Inf`.
fn fmt_f(v: f64, width: usize, prec: usize) -> String {
    if v.is_infinite() {
        let s = if v > 0.0 { "+Inf" } else { "-Inf" };
        return format!("{s:>width$}");
    }
    format!("{v:>width$.prec$}")
}

/// Аналог `%g` из Go: кратчайшее представление, экспонента при exp < -4 или exp >= 6.
pub(crate) fn fmt_g(v: f64) -> String {
    if v.is_nan() {
        return "NaN".into();
    }
    if v.is_infinite() {
        return if v > 0.0 {
            "+Inf".into()
        } else {
            "-Inf".into()
        };
    }
    if v == 0.0 {
        return if v.is_sign_negative() {
            "-0".into()
        } else {
            "0".into()
        };
    }
    let sci = format!("{v:e}");
    let (mantissa, exp) = sci.split_once('e').expect("{:e} always has an exponent");
    let exp: i32 = exp.parse().expect("{:e} exponent is an integer");
    if (-4..6).contains(&exp) {
        format!("{v}")
    } else {
        let sign = if exp < 0 { '-' } else { '+' };
        format!("{mantissa}e{sign}{:02}", exp.abs())
    }
}

impl Task {
    fn scaled_tp_sl(&self) -> (f64, f64) {
        let (mut tp, mut sl) = (self.tp_sl.tp, self.tp_sl.sl);
        if self.decimals > 0 {
            let factor = 10f64.powi(self.decimals);
            tp *= factor;
            sl *= factor;
        }
        (tp, sl)
    }

    /// Ссылка на фронт, который отрисует эту стратегию.
    pub fn url(&self) -> String {
        let (tp, sl) = self.scaled_tp_sl();
        format!(
            "http://localhost/?tf={}&tp={}&sl={}&i1={},{},{}&i2={},{},{}",
            self.timeframe,
            fmt_g(round2(tp)),
            fmt_g(round2(sl)),
            self.ind1.kind,
            fmt_g(round2(self.ind1.coef)),
            self.ind1.source,
            self.ind2.kind,
            fmt_g(round2(self.ind2.coef)),
            self.ind2.source,
        )
    }
}

impl fmt::Display for IndicatorParams {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "{} {:>3} {}",
            clr(&format!("{:>8}", self.kind), HI_MAGENTA),
            self.source,
            clr(&fmt_f(self.coef, 5, 2), HI_CYAN),
        )
    }
}

impl fmt::Display for Task {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let (tp, sl) = self.scaled_tp_sl();
        write!(
            f,
            "{}[{}]  {}[{}]  {}{}  {}{}",
            clr("ind1:", WHITE),
            self.ind1,
            clr("ind2:", WHITE),
            self.ind2,
            clr("tp:", WHITE),
            clr(&fmt_g(round2(tp)), HI_GREEN),
            clr("sl:", WHITE),
            clr(&fmt_g(round2(sl)), HI_RED),
        )
    }
}

impl fmt::Display for TaskResult {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let m = &self.metrics;
        write!(
            f,
            "{} ({})  {} ({})  [{}{}  {}{}]  {}  {}",
            clr(
                &format!("+{}%", fmt_f((m.coef - 1.0) * 100.0, 7, 2)),
                HI_GREEN
            ),
            clr(&format!("{:3}", m.wins), HI_GREEN),
            clr(&format!("{}%", fmt_f(m.max_drawdown, 5, 2)), HI_RED),
            clr(&format!("{:3}", m.losses), HI_RED),
            clr("pr/dd:", WHITE),
            clr(&fmt_f(m.profit_to_dd, 5, 2), HI_CYAN),
            clr("pr/cnd:", WHITE),
            clr(&fmt_f(m.profit_to_candles, 6, 4), HI_YELLOW),
            self.task,
            self.task.url(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fmt_g_matches_go() {
        assert_eq!(fmt_g(900.0), "900");
        assert_eq!(fmt_g(9.5), "9.5");
        assert_eq!(fmt_g(0.1), "0.1");
        assert_eq!(fmt_g(0.0001), "0.0001");
        assert_eq!(fmt_g(0.00001), "1e-05");
        assert_eq!(fmt_g(123456.0), "123456");
        assert_eq!(fmt_g(1_000_000.0), "1e+06");
        assert_eq!(fmt_g(1_234_567.0), "1.234567e+06");
        assert_eq!(fmt_g(0.0), "0");
    }

    #[test]
    fn fmt_f_matches_go() {
        assert_eq!(fmt_f(1.23456, 5, 2), " 1.23");
        assert_eq!(fmt_f(f64::INFINITY, 5, 2), " +Inf");
        assert_eq!(fmt_f(-1.5, 7, 2), "  -1.50");
    }
}
