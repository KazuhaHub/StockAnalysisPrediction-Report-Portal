# Completed-session adjustment boundary evidence

Read-only Tencent captures made on 2026-10-26 from the local development egress.
These are vendor observations, not official exchange references or production-egress probes.
The original response bytes are preserved. No prices, dates, snapshots or rows were rewritten.

Host/path: `https://web.ifzq.gtimg.cn/appstock/app/fqkline/get`
Headers: `User-Agent: Mozilla/5.0`, `Referer: https://gu.qq.com/`.

| File | `param` | Last row | SHA-256 |
|---|---|---|---|
| `tencent_qfq_sh600519_end_20261026.json` | `sh600519,day,2026-10-19,2026-10-26,640,qfq` | 2026-10-23 | `e09dd25641f2912fd8f8bd6e172a95b73c104412b529861687547b69e5deeeeb` |
| `tencent_qfq_sh600519_end_20261027.json` | `sh600519,day,2026-10-19,2026-10-27,640,qfq` | 2026-10-26 | `97466730e8370a6c8ea2cfd6e24f632f3e942be08f5a8753d7cbbe3c3713cdb8` |
| `tencent_bfq_sh600519_end_20261027.json` | `sh600519,day,2026-10-19,2026-10-27,640,bfq` | 2026-10-26 | `85d0e2045636c314e588440dcd0c14d96be791d62accb0450ce9c71d993a7952` |

Both adjusted responses carry a snapshot stamped 2026-10-26 16:14:50 Shanghai.
The request ending on October 26 stops on October 23; the request ending on October 27
includes October 26 at 1349.98. October 19 is 1324.97 in both adjusted responses.
The unadjusted capture independently contains the same two closes. A current snapshot alone
therefore does not prove the adjusted candle window covers the completed due session.

The adapter requests through the next civil day and filters parsed rows back to the caller's
inclusive window. The regression replays both captured boundaries through the actual adjustment
fetch entry point and the dry evaluator. Its index calendar is explicitly synthetic; it is not
an official market-data cross-check. A separate deliberate mutation verifies that an extra
end-date row cannot escape into the accepted adjustment map.

This evidence does not establish that every historical Tencent range is end-exclusive, that
all vendors share this behavior, or that the captured prices are official reference closes.
Production acceptance still requires an updated deployment, a bounded dry pass and independent
official starting/due stock and benchmark close checks. Historical ledger mismatches remain intact.
