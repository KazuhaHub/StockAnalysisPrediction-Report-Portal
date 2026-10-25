# Public quote source evidence — 2026-10-25

These are bounded local-egress measurements, not production availability guarantees or settlement
source approvals. No production configuration or database shape was changed.

## Sohu historical display

The new adapter passed the existing fixed-panel capture for the two sessions 2026-10-22 and
2026-10-23: seventeen Shanghai/Shenzhen stocks and three benchmark indices. Three Beijing targets
were rejected as unsupported before HTTP. Separate requests measured stock `600081` and benchmark
`000905`. The source supplies dates without live observation timestamps. Rounded lot volume also
loses share precision. ADR 0044 defines its default-off, historical-display-only admission.

Primary reference: [Sohu historical quotes](https://q.stock.sohu.com/cn/600081/lshq.shtml).

## BaoStock feasibility probe

The published BaoStock 0.9.4 Python SDK was installed in a temporary dependency directory, using
the existing Python 3.14.3 and pandas 3.0.3 runtime. Anonymous login succeeded. One connection made
five serial daily-history queries, separated by two seconds, then logged out. Each query used
`adjustflag="3"` and returned both requested sessions with success code `0`.

| Target | 2026-10-22 close | 2026-10-23 close |
| --- | ---: | ---: |
| `sh.600081` | 9.8200 | 10.1300 |
| `sh.000852` | 6578.2722 | 6734.5467 |
| `sh.000906` | 4482.4376 | 4567.2567 |
| `sh.000300` | 4036.2966 | 4105.3286 |
| `sh.000905` | 6655.2498 | 6814.4429 |

Rounded to the portal's integer-cent display precision, these closes agree with the measured Sohu
rows. BaoStock's stock volume was 6,976,730 shares for 2026-10-23; Sohu's 69,767 lots becomes
6,976,700 shares. This illustrates why close agreement alone is insufficient evidence equivalence.

The [official access rules](https://www.baostock.com/blacklist) state a daily maximum of 50,000 API
requests and prohibit concurrent connections. The first restriction in a natural year freezes
access for six hours; subsequent restrictions grow by the annual restriction count times six
hours. This is a ceiling, not a target dispatch rate. Pagination and other clients sharing egress
must be included in any capacity calculation.

The SDK uses a persistent TCP connection on port 10030, not an HTTP endpoint. Its current published
transport does not set its own connection/read deadline; this probe supplied an eight-second socket
default and a forty-second outer process deadline. This is a separate deployment concern from the
portal's HTTP transport scheduler.

### Follow-up requirements

- Repeat coverage and connectivity checks from the production egress, including suspended stocks
  and corporate-action dates. No production-egress measurement is implied by this probe.
- Serialize connections across all local consumers and coordinate the daily budget with Dify or
  other clients using the same egress. An independent SDK process per request would violate this.
- Choose and review the runtime boundary before integration: the portal currently ships one Go
  binary, while this measured client is Python over TCP. Any adapter needs bounded cancellation,
  reconnect behavior and actual request accounting outside the HTTP-only transport hook.
- Admit historical display separately from settlement. BaoStock daily rows in this probe do not
  carry a live observation timestamp or prove corporate-action completeness.

BaoStock is a measured historical-data candidate. It is not compiled into this release and remains
ineligible for live quotes or forecast settlement.
