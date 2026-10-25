# Sohu history fixtures

Captured on 2026-10-25 from `https://q.stock.sohu.com/hisHq`, with `start=20261022`,
`end=20261023`, `stat=1`, `order=D` and `period=d`. The source codes are `cn_600081` and
`zs_000852`. User-Agent is `Mozilla/5.0`; Referer is `https://q.stock.sohu.com/`.

Only `status`, `code` and `hq` are retained. The aggregate `stat` field is unused and omitted.
The stock has eleven row columns, including post-market volume; the index has ten. Column order:
date, open, close, change, change percentage, low, high, volume in lots, amount in ten-thousand
CNY, turnover percentage, optional post-market volume. The adapter consumes the first nine.

The vendor supplies civil dates without observation timestamps. These fixtures do not establish
current-session completion, corporate-action completeness, exact share-volume precision or any
settlement authority. See ADR 0044 for display admission and the broader fixed-panel measurement.
