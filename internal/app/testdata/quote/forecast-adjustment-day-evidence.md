# Tencent day-only adjustment response evidence

Read-only public responses captured on 2026-10-29 from local development egress.
These are vendor schema observations, not official close references or production-egress tests.
The complete response bytes below are unchanged. Header: `Referer: https://gu.qq.com/`.
Host/path: `https://web.ifzq.gtimg.cn/appstock/app/fqkline/get`.

| File | param | SHA-256 |
|---|---|---|
| `tencent_qfq_day_sh688469.json` | `sh688469,day,2026-10-20,2026-10-28,1500,qfq` | `328895ec8420dcb38f1cc9a662dcca3d3ef31fab763e30a22ca7791d8aa64a09` |
| `tencent_qfq_day_sh688825.json` | `sh688825,day,2026-10-22,2026-10-30,1500,qfq` | `f92829ad8af9d038c2f53620d2caa065f7e9679206582ea2ad4df08a9e8df3a4` |

Both explicit qfq requests return code 0, a matching snapshot and six-field `day` candles,
with `qfqday` absent. The former parser refused these successful responses before any
settlement arithmetic. AKShare's maintained Tencent adapter also recognizes `day` in an
adjusted endpoint response:
https://github.com/akfamily/akshare/blob/main/akshare/stock/quote/stock_hist_tx.py

The detection-only adapter now accepts this absent-key variant. It still prefers explicit
qfqday, refuses empty/null/malformed adjusted arrays, and refuses a day-only response with
extra row annotations: a known or changed ex-event must not be mistaken for unchanged
adjustment ratios. Ordinary display and settlement closes still read day only. These
captures are never evidence to replace unadjusted settlement prices.

Offline tests replay both raw captures through the real adjustment fetch and a dry evaluator
with a synthetic benchmark and ledger. This is not production settlement acceptance.
The production failures for sz001283 and sz300750 remain generic fetch_failed in the old
build; current local qfq probes return qfqday, so their past transport/parser failure is not
attributed to this absent-key defect without production evidence.
