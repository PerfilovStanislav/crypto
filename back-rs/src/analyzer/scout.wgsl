// Быстрый проход перебора на GPU: тот же путь по сделкам, что у `fast::scout`, но в f32.
// Поток = одна пара на одном TP/SL; потоки группы делят одну пару (её маска сигналов всегда
// в кэше) и идут по соседним TP/SL. Пара попадает в `found`, только если её weighted pr/dd
// может превысить `threshold` даже с учётом погрешности f32. Точный расчёт делает CPU.

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
    pad: u32,
}

@group(0) @binding(0) var<uniform> params: Params;
@group(0) @binding(1) var<storage, read> bits: array<u32>;
@group(0) @binding(2) var<storage, read> hot: array<Hot>;
@group(0) @binding(3) var<storage, read> weights: array<f32>;
@group(0) @binding(4) var<storage, read_write> counter: atomic<u32>;
@group(0) @binding(5) var<storage, read_write> found: array<vec2<u32>>;

const NONE: u32 = 0xffffffffu;
// ln 2 = 0.693147180…, округлено вниз.
const LN2_FLOOR: f32 = 0.693147f;
// 2⁻²³ — удвоенная единица округления f32.
const TWO_U: f32 = 1.1920929e-7f;

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
    if (upper <= LN2_FLOOR) {
        return;
    }
    if (dd > 0.0f) {
        // +1e-4 в показателе перекрывает погрешность exp и деления на GPU.
        let bound = (exp(upper + 1e-4f) - 1.0f) / dd;
        if (bound <= params.threshold) {
            return;
        }
    }

    let idx = atomicAdd(&counter, 1u);
    if (idx < params.capacity) {
        found[idx] = vec2<u32>(pair, t);
    }
}
