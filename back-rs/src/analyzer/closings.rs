use crate::config::Direction;
use crate::source::Quotes;

/// Абсолютные отступы тейк-профита и стоп-лосса от цены входа (в единицах цены).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct TpSlParam {
    pub tp: f64,
    pub sl: f64,
}

/// Исход сделки, открытой по цене `open[i]`, для каждой свечи `i`:
/// индекс свечи закрытия (`len` — не закрылась), множитель капитала и просадка.
#[derive(Debug, Clone, Default)]
pub struct TpSlClose {
    pub indexes: Vec<u32>,
    pub coefs: Vec<f64>,
    pub drawdowns: Vec<f64>,
}

/// Лонг (`Buy`): TP — `high >= вход + tp`, SL — `low <= вход - sl`.
/// Шорт (`Sell`) на весь капитал, доход `(вход - выход) / вход`: TP — `low <= вход - tp`,
/// SL — `high >= вход + sl`; множители выходят те же, что у лонга. Если на свече задеты оба
/// уровня, считается SL — как и в лонге, это осторожнее.
pub fn calculate_closings(
    quotes: &Quotes,
    p: TpSlParam,
    commission: f64,
    direction: Direction,
) -> TpSlClose {
    if direction == Direction::Sell {
        return calculate_short_closings(quotes, p, commission);
    }
    let ln = quotes.lows.len();
    let mut c = TpSlClose {
        indexes: vec![0; ln],
        coefs: vec![0.0; ln],
        drawdowns: vec![0.0; ln],
    };
    let cms = (1.0 - commission) * (1.0 - commission);

    'opens: for (i, &opened) in quotes.opens.iter().enumerate() {
        let mut lowest = quotes.lows[i];
        for k in i..ln {
            if lowest > quotes.lows[k] {
                lowest = quotes.lows[k];
            }

            if quotes.lows[k] <= opened - p.sl {
                c.indexes[i] = k as u32;
                c.coefs[i] = cms * (opened - p.sl) / opened;
                c.drawdowns[i] = p.sl / opened;
                continue 'opens;
            } else if quotes.highs[k] >= opened + p.tp {
                c.indexes[i] = k as u32;
                c.coefs[i] = cms * (opened + p.tp) / opened;
                c.drawdowns[i] = 1.0 - lowest / opened;
                continue 'opens;
            }
        }
        // Сделка не закрылась до конца истории. Go-версия здесь заполняла весь хвост `i..`,
        // но каждый следующий `i` всё равно перезаписывал свою ячейку — итог тот же.
        c.indexes[i] = ln as u32;
        c.coefs[i] = 1.0;
        c.drawdowns[i] = 0.0;
    }

    c
}

fn calculate_short_closings(quotes: &Quotes, p: TpSlParam, commission: f64) -> TpSlClose {
    let ln = quotes.lows.len();
    let mut c = TpSlClose {
        indexes: vec![ln as u32; ln],
        coefs: vec![1.0; ln],
        drawdowns: vec![0.0; ln],
    };
    let cms = (1.0 - commission) * (1.0 - commission);

    'opens: for (i, &opened) in quotes.opens.iter().enumerate() {
        let mut highest = quotes.highs[i];
        for k in i..ln {
            highest = highest.max(quotes.highs[k]);

            if quotes.highs[k] >= opened + p.sl {
                c.indexes[i] = k as u32;
                c.coefs[i] = cms * (opened - p.sl) / opened;
                c.drawdowns[i] = p.sl / opened;
                continue 'opens;
            } else if quotes.lows[k] <= opened - p.tp {
                c.indexes[i] = k as u32;
                c.coefs[i] = cms * (opened + p.tp) / opened;
                c.drawdowns[i] = highest / opened - 1.0;
                continue 'opens;
            }
        }
        // Не закрылась до конца истории: остаются значения по умолчанию (len, 1.0, 0.0).
    }

    c
}

/// Грубая проверка, что при таких TP/SL за историю наберётся хотя бы `min_closes` тейк-профитов.
pub fn has_enough_closes(
    quotes: &Quotes,
    p: TpSlParam,
    min_closes: usize,
    direction: Direction,
) -> bool {
    let mut opened = 0.0;
    let mut closes = 0;

    for (i, &v) in quotes.opens.iter().enumerate() {
        if opened == 0.0 {
            opened = v;
        }

        let (take, stop) = match direction {
            Direction::Buy => (
                quotes.highs[i] >= opened + p.tp,
                quotes.lows[i] <= opened - p.sl,
            ),
            Direction::Sell => (
                quotes.lows[i] <= opened - p.tp,
                quotes.highs[i] >= opened + p.sl,
            ),
        };
        if take {
            closes += 1;
            if closes >= min_closes {
                return true;
            }
            opened = 0.0;
        } else if stop {
            opened = 0.0;
        }
    }

    false
}

#[cfg(test)]
mod tests {
    use super::*;

    fn quotes(candles: &[(f64, f64, f64)]) -> Quotes {
        Quotes {
            timestamps: (0..candles.len() as i64).collect(),
            opens: candles.iter().map(|c| c.0).collect(),
            highs: candles.iter().map(|c| c.1).collect(),
            lows: candles.iter().map(|c| c.2).collect(),
            closes: candles.iter().map(|c| c.0).collect(),
            volumes: vec![1.0; candles.len()],
        }
    }

    #[test]
    fn short_take_profit_and_stop_loss() {
        let p = TpSlParam { tp: 5.0, sl: 3.0 };
        // (open, high, low): вход 100, потом рост до 102 и падение до 94 — TP шорта на 95.
        let q = quotes(&[
            (100.0, 101.0, 99.0),
            (100.5, 102.0, 94.0),
            (95.0, 96.0, 94.0),
        ]);
        let c = calculate_closings(&q, p, 0.0, Direction::Sell);
        assert_eq!(c.indexes[0], 1);
        assert!((c.coefs[0] - 1.05).abs() < 1e-12, "coef {}", c.coefs[0]);
        assert!(
            (c.drawdowns[0] - 0.02).abs() < 1e-12,
            "dd {}",
            c.drawdowns[0]
        );
        // Вход 95.0 на последней свече: не закрылась.
        assert_eq!((c.indexes[2], c.coefs[2], c.drawdowns[2]), (3, 1.0, 0.0));

        // Рост до 103 — SL шорта; на той же свече и TP — считается SL.
        let q = quotes(&[(100.0, 103.0, 94.0)]);
        let c = calculate_closings(&q, p, 0.0, Direction::Sell);
        assert_eq!(c.indexes[0], 0);
        assert!((c.coefs[0] - 0.97).abs() < 1e-12 && (c.drawdowns[0] - 0.03).abs() < 1e-12);
    }

    #[test]
    fn has_enough_closes_counts_by_direction() {
        let p = TpSlParam { tp: 5.0, sl: 50.0 };
        let falling = quotes(&[(100.0, 100.0, 94.0), (94.0, 94.0, 88.0), (88.0, 88.0, 82.0)]);
        assert!(has_enough_closes(&falling, p, 3, Direction::Sell));
        assert!(!has_enough_closes(&falling, p, 1, Direction::Buy));
    }
}
