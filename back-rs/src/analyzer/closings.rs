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

pub fn calculate_closings(quotes: &Quotes, p: TpSlParam, commission: f64) -> TpSlClose {
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

/// Грубая проверка, что при таких TP/SL за историю наберётся хотя бы `min_closes` тейк-профитов.
pub fn has_enough_closes(quotes: &Quotes, p: TpSlParam, min_closes: usize) -> bool {
    let mut opened = 0.0;
    let mut closes = 0;

    for (i, &v) in quotes.opens.iter().enumerate() {
        if opened == 0.0 {
            opened = v;
        }

        if quotes.highs[i] >= opened + p.tp {
            closes += 1; // for long positions
            if closes >= min_closes {
                return true;
            }
            opened = 0.0;
        } else if quotes.lows[i] <= opened - p.sl {
            opened = 0.0;
        }
    }

    false
}
