### fix(combos): aggregate combo maxOutput as min, not max

- Combo capabilities now fold `maxOutput` with the same minimum-positive rule as
  `contextWindow`, so a combo never advertises an output ceiling its most
  constrained member cannot serve. Zero/unbounded never wins unless every leaf
  is unbounded.
