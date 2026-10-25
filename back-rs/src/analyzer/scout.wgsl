// Быстрый проход перебора на GPU: тот же путь по сделкам, что у `fast::scout`, но в f32
// (`main`; с мартингейлом — `martingale`, тот же путь, что у `evaluate_martingale`).
// Поток = одна пара на одном TP/SL; потоки группы делят одну пару (её маска сигналов всегда
// в кэше) и идут по соседним TP/SL. Пара попадает в `found`, только если её weighted pr/dd
// может превысить `threshold` даже с учётом погрешности f32. Точный расчёт делает CPU.

// С мартингейлом поля значат другое, см. `martingale`.
struct Hot {
    ln_coef: f32,
    // Просадка, округлённая вниз: оценка pr/dd по ней не меньше точной.
    drawdown: f32,
    // Свеча закрытия; `len` — не закрылась.
    close: u32,
}

struct Params {
    pairs: u32,
    tp_sl: u32,
    len: u32,
    // u32-слов маски сигналов на пару.
    words: u32,
    min_tp: u32,
    capacity: u32,
    // max(3, текущий максимум weighted pr/dd).
    threshold: f32,
    // ln(minWeightedCoef), округлённый вниз.
    min_log: f32,
    // Мартингейл: коэффициент, доля капитала на базу, предел SL подряд (0 — нет), 1 − cms.
    m_coef: f32,
    m_base: f32,
    max_losses: u32,
    one_minus_cms: f32,
}

@group(0) @binding(0) var<uniform> params: Params;
@group(0) @binding(1) var<storage, read> bits: array<u32>;
@group(0) @binding(2) var<storage, read> hot: array<Hot>;
@group(0) @binding(3) var<storage, read> weights: array<f32>;
@group(0) @binding(4) var<storage, read_write> counter: atomic<u32>;
@group(0) @binding(5) var<storage, read_write> found: array<vec2<u32>>;
// Только для `martingale`: цены открытия и cms·tp каждого TP/SL.
@group(0) @binding(6) var<storage, read> opens: array<f32>;
@group(0) @binding(7) var<storage, read> tp_cms: array<f32>;

const NONE: u32 = 0xffffffffu;
// 2⁻²³ — удвоенная единица округления f32.
const TWO_U: f32 = 1.1920929e-7f;
// 2⁻²⁴ — единица округления f32.
const U: f32 = 5.9604645e-8f;
// Погрешность log в WGSL: абсолютная 2⁻²¹ на [0.5, 2], иначе 3 ulp.
const LOG_ABS: f32 = 4.7683716e-7f;
// Флаги в `close` для `martingale`: сделка закрылась по SL / с прибылью (coef > 1).
const SL_BIT: u32 = 0x80000000u;
const WIN_BIT: u32 = 0x40000000u;
const CLOSE_MASK: u32 = 0x3fffffffu;

fn emit(pair: u32, t: u32) {
    let idx = atomicAdd(&counter, 1u);
    if (idx < params.capacity) {
        found[idx] = vec2<u32>(pair, t);
    }
}

// Первая свеча >= pos с сигналом или NONE.
fn next_signal(base: u32, pos: u32) -> u32 {
    var w = pos >> 5u;
    if (w >= params.words) {
        return NONE;
    }
    var word = bits[base + w] & (0xffffffffu << (pos & 31u));
    while (word == 0u) {
        w = w + 1u;
        if (w >= params.words) {
            return NONE;
        }
        word = bits[base + w];
    }
    return (w << 5u) + countTrailingZeros(word);
}

@compute @workgroup_size(64)
fn main(@builtin(global_invocation_id) gid: vec3<u32>) {
    let t = gid.x;
    let pair = gid.y;
    if (pair >= params.pairs || t >= params.tp_sl) {
        return;
    }
    let base = pair * params.words;
    let hbase = t * params.len;

    var pos = 0u;
    var wl = 0.0f;
    var abs_sum = 0.0f;
    var dd = 0.0f;
    var wins = 0u;
    var trades = 0u;
    loop {
        let open = next_signal(base, pos);
        if (open == NONE) {
            break;
        }
        let o = hot[hbase + open + 1u];
        let term = weights[open] * o.ln_coef;
        wl = wl + term;
        abs_sum = abs_sum + abs(term);
        dd = max(dd, o.drawdown);
        wins = wins + select(0u, 1u, o.ln_coef > 0.0f);
        trades = trades + 1u;
        pos = o.close;
    }

    if (wins < params.min_tp) {
        return;
    }
    // Погрешность суммы n слагаемых в f32 (включая округление входов) не больше
    // (n + 3)·u·Σ|слагаемых|; берём 2u и n + 8 с запасом, плюс абсолютный запас.
    let upper = wl + abs_sum * (f32(trades) + 8.0f) * TWO_U + 1e-6f;
    if (upper <= params.min_log) {
        return;
    }
    if (dd > 0.0f) {
        // +1e-4 в показателе перекрывает погрешность exp и деления на GPU.
        let bound = (exp(upper + 1e-4f) - 1.0f) / dd;
        if (bound <= params.threshold) {
            return;
        }
    }

    emit(pair, t);
}

// Серия мартингейла в f32 вместе с оценками относительной погрешности (первого порядка) капитала,
// ставки и убытка серии. Пара отбрасывается, только если точный расчёт её тоже не примет:
// SL подряд и число TP — по точным флагам, ставка и weighted pr/dd — с запасом ×2 на погрешность.
// Если оценка теряет смысл (TP почти не окупает комиссию, капитал около нуля), решает CPU.
//
// Поля `Hot`: `ln_coef` — coef − 1, `drawdown` — просадка сделки, `close` — свеча закрытия
// с флагами `SL_BIT` и `WIN_BIT`.
@compute @workgroup_size(64)
fn martingale(@builtin(global_invocation_id) gid: vec3<u32>) {
    let t = gid.x;
    let pair = gid.y;
    if (pair >= params.pairs || t >= params.tp_sl) {
        return;
    }
    let base = pair * params.words;
    let hbase = t * params.len;
    let a = tp_cms[t];
    let b = params.one_minus_cms;

    var capital = 1.0f;
    var e_cap = 0.0f;
    var peak = 1.0f;
    var e_peak = 0.0f;
    var base_stake = 0.0f;
    var e_base = 0.0f;
    var loss = 0.0f;
    var e_loss = 0.0f;
    var losses = 0u;
    var wl = 0.0f;
    // Абсолютная погрешность wl.
    var err = 0.0f;
    // Нижняя оценка просадки.
    var dd = 0.0f;
    var wins = 0u;
    var pos = 0u;
    loop {
        let open = next_signal(base, pos);
        if (open == NONE) {
            break;
        }
        let o = hot[hbase + open + 1u];
        let g = o.ln_coef;

        var stake = 0.0f;
        var e_stake = 0.0f;
        if (losses == 0u) {
            base_stake = params.m_base * capital;
            e_base = e_cap + 2.0f * U;
            stake = base_stake;
            e_stake = e_base;
        } else {
            // Доход TP: cms·(вход + tp)/вход − 1 = cms·tp/вход − (1 − cms).
            let q = a / opens[open + 1u];
            let gain = q - b;
            let gain_err = 3.0f * U * q + U * b + U * abs(gain);
            if (gain <= 2.0f * gain_err) {
                emit(pair, t);
                return;
            }
            let extra = params.m_coef * loss / gain;
            let e_extra = e_loss + gain_err / (gain - gain_err) + 4.0f * U;
            stake = base_stake + extra;
            e_stake = (base_stake * e_base + extra * e_extra) / stake + U;
        }
        // Ставка больше капитала: с запасом, чтобы точный расчёт её тоже не принял.
        if (stake - capital > 2.0f * (stake * e_stake + capital * e_cap) + 4.0f * U * capital) {
            return;
        }

        let before = capital;
        let e_before = e_cap;
        let delta = stake * g;
        capital = before + delta;
        if (capital <= 0.0f) {
            emit(pair, t);
            return;
        }
        e_cap = (e_before * before + (e_stake + 3.0f * U) * abs(delta)) / capital + U;

        let dd_move = stake * o.drawdown;
        let inner = before - dd_move;
        let worst = min(inner, capital);
        let worst_err = max(e_before * before + (e_stake + 3.0f * U) * dd_move + U * abs(inner), e_cap * capital);
        let d = 1.0f - worst / peak;
        dd = max(dd, d - 2.0f * (worst_err / peak + abs(worst / peak) * e_peak + 3.0f * U));
        peak = max(peak, capital);
        e_peak = max(e_peak, e_cap);

        if ((o.close & SL_BIT) != 0u) {
            let grown = loss - delta;
            e_loss = (e_loss * loss + (e_stake + 3.0f * U) * abs(delta)) / grown + U;
            loss = grown;
            losses = losses + 1u;
            if (params.max_losses > 0u && losses >= params.max_losses) {
                return;
            }
        } else {
            loss = 0.0f;
            e_loss = 0.0f;
            losses = 0u;
        }
        wins = wins + select(0u, 1u, (o.close & WIN_BIT) != 0u);

        // Множитель капитала m = 1 + x; погрешность ln m — от погрешности x и самого log.
        let x = delta / before;
        let e_x = e_stake + e_before + 4.0f * U;
        let m = capital / before;
        let m_low = m - abs(x) * e_x;
        if (m_low <= 0.0f) {
            emit(pair, t);
            return;
        }
        let lm = log(m);
        let w = weights[open];
        wl = wl + w * lm;
        err = err + w * (abs(x) * e_x / m_low + LOG_ABS + 4.0f * U * abs(lm)) + U * abs(wl);

        pos = o.close & CLOSE_MASK;
    }

    if (wins < params.min_tp) {
        return;
    }
    let upper = wl + 2.0f * err + 1e-6f;
    if (upper <= params.min_log) {
        return;
    }
    if (dd > 0.0f) {
        // +1e-4 в показателе перекрывает погрешность exp и деления на GPU.
        let bound = (exp(upper + 1e-4f) - 1.0f) / dd;
        if (bound <= params.threshold) {
            return;
        }
    }
    emit(pair, t);
}
