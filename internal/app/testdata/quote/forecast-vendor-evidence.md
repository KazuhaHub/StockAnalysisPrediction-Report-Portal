# Forecast vendor evidence

Read-only Tencent captures on 2026-10-22 UTC, authorized by owner decision V20. Each JSON file is
the complete UTF-8 response, with one terminal LF added; no rows, snapshots, event elements or
prices are rewritten. These are new evidence fixtures, not regenerated forecast golden outputs.
Tests replay them offline through the real parser and adjustment fetch entry point.

## Capture requests

Host/path: `https://web.ifzq.gtimg.cn/appstock/app/fqkline/get`
Header: `Referer: https://gu.qq.com/`. The `param` query value is below. The captured windows fit
inside 640 bars; the application's current fetch ceiling is 800. The snapshot is current vendor
metadata, not a historical snapshot from the candle window. Its stamp must not be interpreted as
the capture clock or as evidence about historical suspensions.

| File suffix after `tencent_fqkline_` | `param` | Rows |
|---|---|---|
| `bfq_sz001323.json` | `sz001323,day,2025-08-11,2025-08-25,640,bfq` | 11 |
| `qfq_sz001323.json` | `sz001323,day,2025-08-11,2025-08-25,640,qfq` | 11 |
| `bfq_sh000852.json` | `sh000852,day,2025-08-11,2025-08-25,640,bfq` | 11 |
| `bfq_sh600221.json` | `sh600221,day,2024-09-01,2024-10-31,640,bfq` | 37 |
| `qfq_sh600221.json` | `sh600221,day,2024-09-01,2024-10-31,640,qfq` | 37 |
| `bfq_sz301308.json` | `sz301308,day,2024-09-01,2024-10-31,640,bfq` | 37 |
| `bfq_sh689009.json` | `sh689009,day,2024-09-01,2024-10-31,640,bfq` | 37 |

## Evidence and limits

- **Capital-reserve transfer, 001323, 2025-08-21.** The issuer's implementation notice,
  [2025-051, published through the Shenzhen exchange](https://disc.static.szse.cn/download/disc/disk03/finalpage/2025-08-14/61a6268c-eeb7-40e1-bc58-00811fb85fd5.PDF),
  documents a transfer of one share per ten, excluding repurchased shares, with no cash dividend.
  The captured seventh element independently identifies the date and transfer; `fh_sh` is empty,
  so the cash-dividend parser supplies no ExDates signal. Raw closes are 32.58 and 30.15; adjusted
  closes round to 28.19 and 28.71. The raw move is -7.46%, inside the daily-limit backstop, while
  the ratio changes decisively. This is not an invented exact -9.09% theoretical ex-price: actual
  trading also moves the close. The historical adjusted levels reflect later adjustments at the
  capture date; only the change of ratio is used, never the adjusted prices as returns.
- **Ordinary low-price limit-down control, 600221, 2024-10-09.** Raw and adjusted closes both move
  from 1.32 to 1.19. The unchanged ratio must not flag an adjustment. On 2024-10-31, the raw move
  from 1.25 to 1.38 is +10.40% but is inside the fen-rounded limit. No ST-specific limit is assumed.
- **Board routing controls.** 301308 moves 88.87 to 106.64 on 2024-10-08; 689009 moves 48.20 to
  56.80 on the same date. Both exceed the old incorrect 10% backstop but are inside 20%.
- **Known P7 gaps remain.** These bodies do not prove a live suspension, a delisted-stock answer,
  a Beijing 30% day or listing-day behavior. Probes for old/new Beijing codes 830799, 920799 and
  920185 over the 2024 window returned empty `day` arrays, so they were not substituted with
  fabricated data or presented as trading-day evidence. Existing synthetic guards remain useful
  but do not close these real-vendor gaps. Rights-issue evidence is also not claimed here.

## Stored-file SHA-256

| Suffix | SHA-256 (including the terminal LF) |
|---|---|
| `bfq_sh000852.json` | `b9ba696e85333c56bd490057fcbba4b5742bbf5505d1a68d65ec799e162ebb45` |
| `bfq_sh600221.json` | `bc55b27efaaafbfcbdeeaea9ffff05eaa82b492b5fe54a00c75e1efb395071a6` |
| `bfq_sh689009.json` | `0afbd598e7bcfe30e2c5e4601741ca680eff5a0a09ebed43e6fc285afaf956b9` |
| `bfq_sz001323.json` | `4c70010916add85efc4dc8ed0f59d566b87794b35fb6a32393ae4067e7fd2e1e` |
| `bfq_sz301308.json` | `7f35efd2b3ddd45afc4a2f3cbe0eb51ef53b8d67ba46dc96eb46a285930991f1` |
| `qfq_sh600221.json` | `2a1629c1591a6f68ab30ebbc7f9c943f3c9b6dd7ba9e68589a8a04ea81687a48` |
| `qfq_sz001323.json` | `a82f5af3289d738a48b587e8e0697f8fe764da95b66885669d040828b1caa5a0` |
