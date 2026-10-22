#!/usr/bin/env python3
"""Строка результата анализатора -> поля стратегии для trader/config.yaml.

    ./strategy_from_line.py '+ 348.49% ( 41) ... ind1:[ dema C 1.09]  ind2:[ dema LC 1.19]  tp:305  sl:247'
    pbpaste | ./strategy_from_line.py

symbol, timeframe, deposit_percent и decimals (из configs/buy/<PAIR>.yaml) добавьте сами.
Проверка: python3 -m doctest strategy_from_line.py

>>> print(to_yaml('+ 348.49% ( 41)   9.47% ( 24)  [pr/dd:36.81  pr/cnd:0.2599]  '
...               'ind1:[    dema   C  1.09]  ind2:[    dema  LC  1.19]  tp:305  sl:247'))
take_profit: 305
stop_loss: 247
indicator1:
  type: "dema"
  source: "C"
  coef: 1.09
indicator2:
  type: "dema"
  source: "LC"
  coef: 1.19
"""
import re
import sys


def to_yaml(line: str) -> str:
    line = re.sub(r"\x1b\[[0-9;]*m", "", line)  # цвета из терминала
    inds = re.findall(r"ind([12]):\[\s*(\w+)\s+(\w+)\s+([\d.]+)\]", line)
    tp = re.search(r"\btp:([\d.]+)", line)
    sl = re.search(r"\bsl:([\d.]+)", line)
    if len(inds) != 2 or not tp or not sl:
        raise ValueError(f"not an analyzer result line: {line.strip()!r}")

    out = [f"take_profit: {tp[1]}", f"stop_loss: {sl[1]}"]
    for n, kind, source, coef in inds:
        out += [f"indicator{n}:", f'  type: "{kind}"', f'  source: "{source}"', f"  coef: {coef}"]
    return "\n".join(out)


if __name__ == "__main__":
    text = " ".join(sys.argv[1:]) or sys.stdin.read()
    try:
        print(to_yaml(text))
    except ValueError as e:
        sys.exit(str(e))
