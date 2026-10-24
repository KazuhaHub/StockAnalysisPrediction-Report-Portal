# Live suspension and delisted-instrument evidence

Read-only V20 captures on 2026-10-22 UTC. These supplement
[the original adjustment/board captures](forecast-vendor-evidence.md). Each file preserves the
complete response bytes with one terminal LF added. No forecast golden output is regenerated.

Endpoint: `https://web.ifzq.gtimg.cn/appstock/app/fqkline/get`, with
`Referer: https://gu.qq.com/`. Query parameter and row count:

| File suffix after `tencent_fqkline_` | `param` | Rows |
|---|---|---|
| `halt_sh601326.json` | `sh601326,day,2026-09-01,2026-10-22,800,bfq` | 30 |
| `halt_sz301314.json` | `sz301314,day,2026-09-01,2026-10-22,800,bfq` | 24 |
| `halt_qfq_sz301314.json` | `sz301314,day,2026-09-01,2026-10-22,800,qfq` | 24 |
| `halt_sh000852.json` | `sh000852,day,2026-09-01,2026-10-22,800,bfq` | 32 |
| `delisted_sh600485_recent.json` | `sh600485,day,2025-09-01,2026-10-22,800,bfq` | 0 |
| `delisted_sh600485_2019.json` | `sh600485,day,2019-01-01,2019-12-31,800,bfq` | 117 |

## Independent identity and event evidence

- **601326:** the issuer's
  [2026-027 suspension announcement](https://static.cninfo.com.cn/finalpage/2026-10-21/1225612840.PDF)
  identifies the suspension beginning 2026-10-21. The captured series ends on 2026-10-20 at 3.57;
  its vendor stamp is 2026-10-22 16:14:52 Shanghai. The 2026-10-21 checkpoint has only one later
  completed index session, so three-session confirmation cannot justify its halt. The current
  stamp can, even with the pass-wide lag valve set. Removing the stamp must refuse `stale_bar`,
  or `calendar_lag` with the valve set. Counterfactual input changes are test mutations, not
  substituted captures.
- **301314:** the issuer's
  [2026-027 progress announcement](https://www.tpyzq.com/main/corporatebusiness/detail/index.shtml?dataId=306442)
  records suspension from 2026-10-13. Raw and adjusted series end on 2026-10-12 at 46.78, with
  the raw vendor stamp at 2026-10-22 16:14:39 Shanghai. The test classifies and writes a prediction
  received after the close on October 12, then runs the real evaluator and transactional writes.
  S0 is October 13; the baseline uses the last traded October 12 close. d5 is October 20, still
  inside the halt: close 46.78, return 0, halted true, corporate action false, source Tencent.
  Benchmark closes are 7023.22 on S0 and 6766.90 on d5, giving -365 bp. Incorrectly starting the
  benchmark on base_date's 7021.47 gives -363 bp and fails the test.
- **600485:** the exchange's
  [issuer announcement catalogue](https://www.sse.com.cn/assortment/stock/list/info/announcement/index.shtml?productId=600485)
  records the 2021-05-26 termination announcement. The recent body has no candles but still
  prints a snapshot of 1.39 with a 2026-10-22 09:00 stamp. An older query supplies 117 genuine
  2019 candles. Neither the current-looking snapshot nor those old candles supplies a 2026
  baseline in the pure baseline resolver. A full evaluator replay of the empty recent body
  preserves NULL s0_fen and no resolved marks, records an attempt-free source failure, and makes
  no adjustment call. The parser does not infer delisting
  from a symbol: empty history remains a delivery fault, not proof permitting finalization.

## Guard effectiveness and limits

All tests first fail without their captured files. After adding the files, a temporary benchmark
anchor mutation to base_date failed with -363 rather than -365; removing current-stamp evidence
failed the short-halt positive control; manufacturing a candle from the delisted snapshot failed
with a fabricated 139-fen baseline. All three production mutations were reverted before validation.
No runtime behavior change is part of this evidence follow-up.

This does not prove that a delisted instrument with no available history can be settled, nor
that Tencent provides all historical windows. The conservative source-failure behavior is pinned,
not relabelled as successful settlement. Beijing 30%/listing-day and rights-issue captures remain
outstanding; a future real long-halt window may further strengthen bounded-look-back coverage.

## Stored-file SHA-256

| Suffix | SHA-256 (including terminal LF) |
|---|---|
| `halt_sh601326.json` | `f4808aa18e04e4c5d97cbff8f5f2bfcb6417d9eac5e83f68da50f691401ae18f` |
| `halt_sz301314.json` | `bb495380484df8b1ff26460a6a25c39bd5c20849b9850e7cbb0009e926eb03f0` |
| `halt_qfq_sz301314.json` | `dc73a9d16dd457e8576430cac6a95b40013f267cd69ba007f11ffbec9b71360d` |
| `halt_sh000852.json` | `046566f61f53e7dc12cd29f8325ec4c9edce86d2972ab5e70158965642e2ad4d` |
| `delisted_sh600485_recent.json` | `d62e9ac3de389050eb02566219036d089f5962954f5c5611e12de5d0b9aa1e52` |
| `delisted_sh600485_2019.json` | `f2ccae89ad4db4803d82f2b8fbfdf0baf82c3bcd5f7f51fb34bd3b43a069d820` |
