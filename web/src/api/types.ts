// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
  name?: string // display name, falls back to username
  admin: boolean
  role?: string
  perms?: Record<string, boolean>
  email?: string // the user's email (for the "email me when done" opt-in)
  mail_enabled?: boolean // whether SMTP is configured, so email features can be offered
  // Security state, so the account page can branch before the user submits (ADR 0023).
  federated?: boolean // credentials live at the IdP: no local password, no local second factor
  totp_enabled?: boolean
  passkeys?: number
  // Whether this account may ADD a factor (security_policy.go). Policy, not state: an account whose
  // OU has withdrawn enrolment keeps the factor it already has, so the page reads both.
  totp_allowed?: boolean
  passkey_allowed?: boolean
  /** The account must set up a second factor before it can use the portal (see the enrolment gate). */
  must_enroll_2fa?: boolean
  // Navigation hint only; forecast routes always resolve current policy on the server.
  forecast?: { scope: 'all' | 'own' | 'none'; external: boolean }
}

// ---- Batch-run feature ----

export interface PluginInput {
  key: string
  label?: string
  description?: string // explicit help copy; labels are never parsed to infer this
  required?: boolean
  // The declared control kind, straight from Dify's /parameters ("text-input" | "paragraph" |
  // "number" | "select" | "file" | "file-list"). Absent on an older server, and on a manifest
  // plugin that declares no kind — both read as a plain text box, which is what the run form
  // drew for everything before this field existed.
  type?: string
  options?: string[] // the allowed values of a "select" input
}

export interface PluginConfigField {
  key: string
  label?: string
  secret?: boolean
}

export interface BatchPlugin {
  slug: string
  name: string
  version: string
  source: string
  enabled: boolean
  inputs: PluginInput[]
  config: PluginConfigField[]
}

// What the confirm-identity dialog may offer, decided by the server (GET /api/account/stepup/policy).
// The login mode is a deployment policy, so which channels are open is not something the page can
// work out from the account alone: under force-SSO the password box is not drawn at all.
export interface StepUpPolicy {
  password: boolean
  sso: boolean
  providers?: { slug: string; kind: string; name: string; icon?: string }[]
  reason?: string // "sso_required" when the password channel is closed by the login mode
}

// A Dify workflow input field, discovered via /parameters (docs/adr/0006-dify-native.md).
export interface DifyInput {
  variable: string
  label?: string
  display_label?: string // optional portal-owned short label; Dify's source label remains intact
  description?: string // portal/Dify metadata shown behind the field help control
  type?: string
  required?: boolean
  options?: string[]
}

export interface BatchTarget {
  id: number
  plugin_slug: string
  plugin_name?: string
  name: string
  created_at: string
  mode?: string // Dify app mode: "" / "workflow" / "chat"
  inputs?: PluginInput[]
  collapse_optional_inputs?: boolean // one-run form starts with non-required inputs folded
  // Surfaces an admin allows this target on. The API always sends the resolved list, so an
  // unset target arrives as all four rather than as an empty array the UI would have to
  // special-case.
  surfaces?: Surface[]
}

// A place a target can be picked. The run queue is not one: it lists jobs that already
// exist and never chooses a target.
export type Surface = 'run' | 'batch' | 'recurring' | 'chat'
export const ALL_SURFACES: Surface[] = ['run', 'batch', 'recurring', 'chat']

// One target's row in the pull-from-Dify preview (POST /api/admin/batch/dify/refresh).
//
// The preview writes nothing. `inputs` is exactly what a confirm would store — and it is the ONLY
// thing a confirm stores: remote_name is shown so an admin can tell the key still points at the
// workflow they think it does, and is never written over the name they chose.
export interface DifyRefreshResult {
  id: number
  local_name: string
  remote_name?: string
  local_mode?: string
  remote_mode?: string
  inputs?: DifyInput[]
  /** What the workflow declares as the portal's own; written back on confirm. */
  reserved_inputs?: string[]
  added?: string[]
  removed?: string[]
  required_changed?: string[]
  reordered?: boolean
  changed?: boolean
  name_differs?: boolean
  // Losing the stock-code input silently disables same-day reuse (ADR 0022) rather than failing.
  symbol_input_lost?: string
  error?: string        // the probe failed outright; nothing to apply
  inputs_error?: string // connected, but /parameters did not answer — an empty list is not an answer
}

// A Dify target's editable config, returned by GET /api/admin/batch/dify/targets/{id}.
// The api_key is never sent back — has_key only reports whether one is stored.
export interface DifyTargetEdit {
  id: number
  name: string
  base_url: string
  mode?: string // "" / "workflow" / "chat"
  inputs: DifyInput[]
  has_key: boolean
  // External-tenancy declarations (ADR 0022): the report type this workflow produces, and which
  // input key carries the stock code. Both are required for same-day reuse to apply.
  output_subtype?: string
  symbol_input?: string
  collapse_optional_inputs?: boolean
  /** Dify inputs reserved for the portal (_rp_*); never part of the user-facing list. */
  reserved_inputs?: string[]
}

// Queue summary for the home banner + drawer (docs/adr/0007-run-analysis-and-scheduling.md).
export interface BatchQueueSummary {
  waiting: number // due, awaiting admission (excludes not-yet-due scheduled)
  running: number // jobs with active work; excludes preset-window waits between batch rows
  running_rows?: number // concurrent runs (rows) executing now — what the run cap governs
  scheduled: number // jobs waiting for a future time or outside an inverted preset window
  budget: number // max concurrent runs (rows) allowed at once
  reserved: number // slots held for urgent runs
  my_priority?: number // the caller's resolved base priority (0..100, ADR 0008)
  done_today?: number // terminal jobs finished today (server-side count; exact under the paginated list)
}

export interface BatchJob {
  id: number
  target_id: number
  surface?: Extract<Surface, 'run' | 'batch' | 'recurring'> // submission source shown separately from run timing
  status: string
  priority?: string // "urgent" or a base number 0..100 as a string (ADR 0008)
  run_mode?: RunMode
  avoid_window?: boolean
  window_blocked?: boolean
  run_at?: string // one-shot scheduled start ("" = ASAP)
  scheduled?: boolean // queued but not yet due (定时, waiting for run_at)
  inputs?: string // first row's inputs as a JSON string (for a 标的 label)
  ahead?: number // for a queued job: how many are ahead of it in the queue
  concurrency: number
  max_retries: number
  total: number
  succeeded: number
  partial: number
  failed: number
  cancelled?: number // rows the operator cancelled individually (terminal, neither ok nor fail)
  created_by: string
  created_at: string
  started_at: string
  finished_at: string
}

export interface BatchItem {
  id: number
  row_index: number
  inputs: string
  status: string
  attempts: number
  run_id: string
  conversation_id: string // Dify chat/agent reconcile handle (empty for workflow runs)
  task_id: string // Dify task id (server-side stop / tracing)
  error: string
  started_at: string
  finished_at: string
}

// Interactive chat/assistant (docs/adr/0012). A ChatTarget is a Dify chat/agent app;
// a ChatConversation is the portal's thin index row (Dify holds the messages).
export interface ChatTarget {
  id: number
  name: string
  mode: string // Dify app mode (e.g. "agent-chat", "chat")
}

export interface ChatConversation {
  id: number
  target_id: number
  title: string
  starred: boolean // pinned to the top of the user's list
  created_at: string
  updated_at: string
  started: boolean // has a Dify conversation_id yet (i.e. at least one turn sent)
}

export interface ChatTurn {
  query: string // the user's message
  answer: string // the assistant's reply
  created_at: number
}

export interface BatchJobDetail {
  job: BatchJob
  counts: { queued: number; running: number; succeeded: number; partial: number; failed: number; cancelled: number }
  running_in_process: boolean
  items: BatchItem[]
}

export interface Webhook {
  id: number
  url: string
  events: string[]
  active: boolean
  created_at: string
  has_secret: boolean
  last_status: number
  last_error: string
  last_delivered_at: string
}

export interface WebhooksResp {
  webhooks: Webhook[]
  events: string[]
}

export interface SymbolInfo {
  symbol: string
  name: string
  count: number
  latest: string
}

export interface Rep {
  id: number
  title: string
  displayTitle: string // title with the as-of company name folded in ("001696 宗申动力 投资决策建议"); server-computed so it matches the MD/PDF download filenames
  symbol: string
  name: string // as-of company name (snapshot at ingest)
  curName?: string // current company name; differs after rename / backdoor listing
  date: string
  time?: string // UTC RFC3339 ingest instant; legacy rows are date-only/empty
  kind: string
  rtype: string
  source: string
  md: string
  html: string
}

export interface GroupMember {
  id: number
  rtype: string
  kind: string
  title: string
}

export interface Group {
  key: string
  symbol: string
  market?: QuoteResp['market']
  name: string // as-of company name (snapshot)
  curName?: string // current company name; differs after rename / backdoor listing
  title?: string // fallback display title for thematic reports with no stock code/name
  date: string
  time?: string // latest ingest instant in the run (when pushed to the portal; UTC RFC3339)
  kind: string
  kinds: string[]
  src: string // "new" | "old"
  n: number
  members: GroupMember[]
}

export interface FavoriteReport {
  id: number
  name: string
  title: string
  date: string
}

export interface FavoriteItem {
  market: QuoteResp['market']
  symbol: string
  key: string
  ord: number
  report?: FavoriteReport
}

export interface FavoritesResp {
  items: FavoriteItem[]
  limit: number
}

export interface HomeResp {
  groups: Group[]
  newTotal: number
  oldTotal: number
  totalRuns: number
  page: number
  pages: number
  size: number
  types: string[]
  kinds: string[] // 大类 (top-level categories) for the home filter
  // Written forms present in what this reader can see (ADR 0024), for the version filter. Empty
  // when there is only one — a filter whose every setting means the same thing is not a filter.
  versions: { name: string; label: string }[]
  links: LinkItem[]
  linkGroups: LinkGroup[] // named, foldable groups of entry buttons
  kindColors: Record<string, string> // 大类 → antd Tag preset color, admin-configured
}

export interface TimelineNode {
  date: string
  n: number
}

export interface SubTab {
  id: number
  label: string // the report type plus generator version when the producer supplied one
  title: string // the report this tab opens, server-composed (same string as the reader heading)
  rtype: string
  version?: string // which written form this tab is (ADR 0024)
}

export interface StockResp {
  symbol: string
  market?: QuoteResp['market']
  name: string
  selDate: string
  selKind: string
  selId: number
  timeline: TimelineNode[]
  kinds: string[]
  subtabs: SubTab[]
  rep: Rep | null
}

export interface RunResp {
  key: string
  symbol: string
  name: string
  date: string
  selId: number
  tabs: SubTab[]
  rep: Rep | null
}

export interface LinkItem {
  id: number
  label: string
  url: string
  icon?: string
  newTab?: boolean // open in a new tab (default true)
  groupId?: number // the group it belongs to, or 0/undefined = ungrouped (top-level, inline)
  ord: number
  visible?: boolean // shown on the home page (default true); hidden entries stay in the admin list
}

// How a link group renders on the home page.
export type LinkGroupMode = 'row' | 'expand' | 'popover' | 'modal'

export interface LinkGroup {
  id: number
  name: string
  mode: LinkGroupMode // row (own always-visible row) | expand | popover | modal
  showLabel: boolean // show the group name (mainly for row mode)
  icon?: string // icon name shown on the group's trigger button (empty = default folder glyph)
  ord: number
  visible?: boolean // shown on the home page (default true); hidden groups stay in the admin list
}

export interface TypeRow {
  name: string
  kind: string
  ord: number
  isSummary: boolean
  label: string
}

export interface TypeGroup {
  kind: string
  rows: TypeRow[]
}

export interface TypesResp {
  groups: TypeGroup[]
  kinds: string[]
  colors: Record<string, string> // 大类 → antd Tag preset color, admin-configured
}

export interface Role {
  code: string
  name: string
}

export interface UserRow {
  username: string
  role: string
  display_name?: string
  email?: string
  active: boolean
  last_login?: string
  /** Last authenticated request, throttled server-side. A different fact from last_login. */
  last_seen?: string
  expires_at?: string // account validity cutoff as a panel-tz civil date "YYYY-MM-DD"; "" = never (ADR 0022 R4)
  primary_group: number // primary group id, or 0 when the user inherits the Default group
  federated?: boolean // signs in through an identity provider; has no usable local password
  sso_slug?: string // which provider, when federated
}

export interface UserGroupRow {
  id: number
  name: string
  description?: string
  is_default?: boolean // the fallback group inherited by users with no primary group
  // weight / urgent_unlimited are null when this group inherits the Default group's
  // value (group model B); a value means this group overrides it.
  weight: number | null // urgent tickets granted per period to each member (ADR 0005)
  urgent_unlimited?: boolean | null // members can run urgent jobs without spending tickets
  // Per-group governance (group model B): null = inherit the Default group.
  allow_urgent?: boolean | null // may members use the urgent lane at all
  max_queued?: number | null // cap on active (queued+running) runs per member; 0 = unlimited
  run_window?: string | null // '' = any hour, else 'H1-H2' (panel timezone)
  priority?: string // base run priority 0..100 override ('' / undefined = inherit the system default; ADR 0008)
  members: number // primary-member count
  parent_id?: number // where this OU sits in the tree; 0/absent = a root (ADR 0022)
  // External-user tenancy (ADR 0022). restricted is this OU's own flag; restricted_effective also
  // accounts for a restricted ancestor (restriction is sticky down the OU tree).
  restricted?: boolean
  restricted_effective?: boolean
  daily_run_quota?: number | null // run cap for members; null = inherit the parent OU, 0 = unlimited
  /** The window daily_run_quota is measured over: day | week | month | total. '' means day. */
  run_quota_period?: string
  // The per-OU second-factor policy (security_policy.go): null = inherit the parent OU. These say
  // whether the OU's members may ADD a factor — an account that already has one keeps signing in
  // with it whatever this says.
  totp_enroll?: boolean | null
  passkey_enroll?: boolean | null
  /** Members of this OU must have a second factor. Sticky down the tree. */
  require_2fa?: boolean | null
}

// Per-OU run allow-list matrix (ADR 0022 R3): which workflows a group may run, on which surfaces.
/** One workflow an OU may be allowed to run, and the surfaces the workflow itself permits. */
export interface GroupTargetRow {
  id: number
  name: string
  surfaces: string[]
  output_subtype?: string
}

export interface GroupTargetsResp {
  granted: { target_id: number; surfaces: string[] }[]
  targets: GroupTargetRow[]
}

// Run-quota balance for the run form (ADR 0022 R2). limited=false for internal users,
// admins, and restricted OUs with no cap — the UI then omits the chip entirely.
export interface RunQuota {
  limited: boolean
  limit?: number
  used?: number
  remaining?: number
  period?: string // the window the cap covers: day | week | month | total ('' = day)
  resets_at?: string // next reset as a UTC instant; localized client-side. '' for a lifetime cap,
  // which never refills — the UI must not invent a date for it.
}

// Storage-cleanup console (docs/adr/0017-storage-cleanup.md). Config + last-run summary; retention
// floors clamp the day inputs. freq drives an admin-set daily/weekly/monthly retention pass.
export interface CleanupConfig {
  freq: 'off' | 'daily' | 'weekly' | 'monthly'
  time: string // "HH:MM" panel timezone
  weekday: number // 0=Sun..6=Sat (weekly)
  monthday: number // 1..31 (monthly)
  batch_enabled: boolean
  batch_days: number
  tokens_enabled: boolean
  tokens_grace_days: number
  reports_enabled: boolean
  reports_days: number
  audit_enabled: boolean
  audit_days: number
  // The edit-history target (ADR 0026). revisions_keep is a per-report COUNT enforced when a report
  // is saved, not an age enforced by the retention pass, so it applies whether or not the target is
  // enabled — and 0 means keep every version, which is the shipped state.
  revisions_enabled: boolean
  revisions_days: number
  revisions_keep: number
  // The forecast-verification ledger (ADR 0029 §8). Its floor is short on purpose: the safety of this
  // target is the predicate (orphaned, fully terminal), not the day count.
  predictions_enabled: boolean
  predictions_days: number
  batch_floor: number
  reports_floor: number
  audit_floor: number
  revisions_floor: number
  revisions_keep_max: number
  predictions_floor: number
  last_run_period: string
  last_result: CleanupResult | null
}

// The evaluator's settings and its current backlog (GET/POST /api/admin/forecast, ADR 0029 §6).
//
// `enabled` and `bench` are the only two fields a POST may carry; everything else is a read-only
// count. It is one shape for both verbs because the GET answers with exactly what the page and the
// modal between them need, so a save's reload and a fresh open are the same request.
export interface ForecastSettings {
  /** Whether the evaluator's background loop runs. Off by default. Kept for one release. */
  enabled: boolean
  /** The three-way mode (P9): off, dry (plan, write nothing), or on. */
  mode: '0' | 'dry' | '1'
  /** The last pass, or null before the first. */
  heartbeat?: ForecastHeartbeat | null
  /** The §1.4 readiness list, in order. Item 6 arrives with P23. */
  readiness: ForecastReadinessItem[]
  /** Hand-checked dry runs, keyed by benchmark code. */
  dry_gate: Record<string, ForecastGate>
  /** The benchmark codes the readiness item is about, for the check form. */
  gate_codes: string[]
  /** Email and Slack delivery for the evaluator's alerts (P41). */
  alerts: ForecastAlerts
  /** The index NEW sets are benchmarked against. Sets already written keep theirs, so changing this
   *  never re-scores anything on the ledger. */
  bench: string
  /** A scheduled change, effective from the next cohort (P4). Empty when nothing is pending. */
  bench_next?: string
  bench_next_from?: string
  /** The closed list of benchmarks a new set may be frozen with (P3), so the picker cannot offer one
   *  the server would refuse. */
  bench_codes: string[]
  /** The ledger start (P5): whether one is set, its value, the bound a new value must respect, and
   *  the sets a value at or before the bound would exclude. */
  population: ForecastPopulation
  /**
   * What the loop would find if it ran now. Cheap counts, not fetches, so an admin can tell "on and
   * caught up" (a small or zero backlog) from "on and stuck" (a count that is not moving).
   */
  due_sets: number
  pending: number
  refused: number
  unsettled_baseline: number
  /** Unrecorded covered reports since the ledger start (P2), and ledger store failures in 30 days.
   *  `ok` is false while the ledger start is unset, when a count would be meaningless. */
  unrecorded?: ForecastUnrecorded[]
}

export interface ForecastPassStats {
  kind: 'tick' | 'manual'
  started_at: string
  finished_at: string
  sets: number
  fetches: Record<string, number>
  resolved: number
  halted: number
  refused: Record<string, number>
  refused_by_class: Record<string, number>
  finalised: number
  healed: number
  baselines: Record<string, number>
  awaiting_session: number
  due_unsettled: number
  oldest_due_unsettled_minutes: number
  stale_valve_tripped: boolean
  errors: number
  last_error?: string
  panics: number
}

export interface ForecastDrySample {
  set_id: number
  mark?: string
  kind: 'settle' | 'baseline' | 'final'
  market: string
  code: string
  source?: string
  due?: string
}

export interface ForecastBenchStat {
  last_bar: string
  fetched_at: string
  source: string
  market: string
  code: string
}

export interface ForecastHeartbeat {
  mode: '0' | 'dry' | '1'
  last_tick_at: string
  pass: ForecastPassStats
  bench: Record<string, ForecastBenchStat>
  dry_sample?: ForecastDrySample[]
  awaiting_gate?: string[]
}

export interface ForecastReadinessItem {
  key: 'binary' | 'population' | 'dry_pass' | 'dry_gate' | 'alerts'
  ok: boolean
  detail?: string
}

export interface ForecastChannelResult {
  at?: string
  ok: boolean
  error?: string
}

export interface ForecastAlerts {
  /** Comma-separated recipients; empty when none are configured. */
  email_to: string
  /** Whether a Slack webhook is stored. The URL itself is never returned. */
  has_slack: boolean
  /** The last delivery per channel, keyed "email" / "slack". */
  last?: Record<string, ForecastChannelResult>
  /** When a test alert last reached both channels. */
  test_ok_at?: string
}

export interface ForecastGate {
  at: string
  by: string
  note: string
}

export interface ForecastUnrecorded {
  rtype: string
  n: number
  since: string
  ok: boolean
  store_failed_30d: number
}

/** The ledger start (P5). `set` is false before an admin has fixed the boundary. */
export interface ForecastPopulation {
  set: boolean
  from?: string
  /** The key exists but its stored value is unreadable (a damaged database); `from` is absent. */
  damaged?: boolean
  /** Who set it and when, from the audit row, when the log still holds it. */
  set_by?: string
  set_at?: string
  /** The latest instant a ledger start may name, derived from the ledger's oldest set. Absent on an
   *  empty ledger, which has no bound to offer. */
  bound?: string
  /** The sets a start at or before `bound` would exclude, oldest first. */
  candidates: ForecastPopulationCandidate[]
}

export interface ForecastPopulationCandidate {
  id: number
  symbol: string
  rtype: string
  made_at: string
  state: string
  orphan: boolean
}

// The outcome of a cleanup pass (also the preview/run response shape).
export interface CleanupResult {
  at: string
  trigger: string // schedule | manual | preview
  dry_run: boolean
  ok: boolean
  error: string
  batch: number
  tokens: number
  reports: number
  audit: number
  revisions: number
  predictions: number
  /** Bytes handed back to the filesystem — the number an admin runs a pass to change. Always 0 on
   *  Postgres, where autovacuum reclaims for reuse and VACUUM FULL would be a full outage. */
  reclaimed: number
  /** "sqlite" | "postgres", so a zero above can be explained rather than guessed at. */
  driver: string
  duration_ms: number
}

export interface CleanupUsageCategory {
  key: string
  rows: number
  bytes: number
  eligible: number
  oldest: string
  newest: string
}

export interface CleanupUsage {
  db_bytes: number
  categories: CleanupUsageCategory[]
}

// One recorded cleanup pass in the audit history.
export interface CleanupRun {
  id: number
  ran_at: string
  trigger: string
  dry_run: boolean
  ok: boolean
  error: string
  batch_deleted: number
  tokens_deleted: number
  reports_deleted: number
  audit_deleted: number
  revisions_deleted: number
  predictions_deleted: number
  bytes_reclaimed: number
  duration_ms: number
}

export interface BatchConfig {
  max_jobs: number
  /** The per-job worker ceiling every requested concurrency is clamped to (ADR 0001). */
  max_concurrency?: number
  reserved_slots: number
  ticket_period_days: number
  default_priority: number
  urgent_enabled?: boolean
  dify_end_user?: string
  dify_poll_seconds?: number // 0 = streaming; >0 = poll the run status every N seconds
  dify_run_timeout_minutes?: number // cap on one run: portal HTTP client + reconcile poll window
  prio_w_base: number
  prio_w_age: number
  prio_w_fair: number
  prio_age_hours: number
  prio_fair_halflife_hours: number
  // Run-form defaults — what the run dialog opens on, edited on its own settings page
  // (Manage → Run defaults). 0 / false means "no default", a supported choice.
  run_default_mode?: RunMode // default mode button: now|preset|scheduled (ADR 0014)
  run_default_idle?: boolean // pre-check "run when queue idle" (immediate mode only)
  run_default_target_id?: number // pre-selected workflow; 0 = none
  run_default_preset_id?: number // pre-picked preset window; 0 = none
  run_default_retries?: number // pre-filled failure retries (0..5)
  run_default_notify?: boolean // pre-tick "email me when done"
  // Not a default but a display choice: does the run form print a preset window's whole rule next
  // to its name, or leave it to the info button beside the picker. Off = just the name.
  run_show_preset_rule?: boolean
}

// Preset low-peak scheduling window (docs/adr/0014-idle-lane-and-preset-windows.md). Which anchor
// fields apply depends on freq: daily uses only time; weekly adds weekday (0=Sun..6=Sat); monthly
// adds day (1..31); yearly adds month (1..12) + day. time is "HH:mm" in the panel timezone.
export type RunMode = 'now' | 'preset' | 'scheduled'
export type RunFreq = 'daily' | 'weekly' | 'monthly' | 'yearly'
export type RunOverrun = 'continue' | 'next' | 'cancel'

export interface RunPresetAnchor {
  weekday?: number
  month?: number
  day?: number
  time: string
}

// One sub-window of a preset; a preset's eligible time is the union of its intervals.
export interface RunPresetInterval {
  start: RunPresetAnchor
  stop: RunPresetAnchor
}

export interface RunPreset {
  id: number
  label: string
  freq: RunFreq
  intervals: RunPresetInterval[]
  on_overrun: RunOverrun
  enabled: boolean
  invert: boolean // true = run OUTSIDE the intervals (they become "do not run" / peak hours)
  ord: number
}

// GET /api/admin/batch/presets — the preset list plus the run-form defaults, in one fetch.
// Every default is optional on the wire: an older server sends only the first two, and a portal
// whose admin has set none of them sends 0 / false, which both read as "no default".
export interface RunPresetsResp {
  presets: RunPreset[]
  default_mode: RunMode
  default_idle: boolean
  default_target_id?: number // pre-selected workflow; 0 = none
  default_preset_id?: number // pre-picked preset window; 0 = none
  default_retries?: number // pre-filled failure retries
  default_notify?: boolean // pre-tick "email me when done"
  show_preset_rule?: boolean // print a window's whole rule beside its name in the picker
}

// Urgent ticket balance for the batch run form (ADR 0005).
export interface BatchTickets {
  unlimited: boolean
  remaining?: number
  allocation?: number
  period_days?: number
  urgent_enabled?: boolean // when false, the run forms hide the urgent control entirely
}

export interface UsersResp {
  users: UserRow[]
  me: string
  roles: Role[]
  groups: UserGroupRow[]
}

export interface SettingsResp {
  oldBase: string
  oldUser: string
  hasPass: boolean
  timezone: string // '' = follow system zone
  // The portal's canonical origin, used for reset links, SSO redirect/ACS URLs, the WebAuthn
  // relying-party id, registration links and the captcha host check.
  publicUrl: string
  siteTitle: string
  siteLogoUrl: string
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  versionDisplay: VersionDisplay
  // How hard an open page is asked to refresh after a deploy.
  updatePromptPolicy: UpdatePromptPolicy
  pwaEnabled: boolean
  pwaIconUrl: string
  // Retained for one release line with the settings POST that still accepts them; announcements
  // are rows now and nothing reads these. Delete them together (ADR 0025).
  announcementEnabled: boolean
  announcementPopup: boolean
  announcementLevel: AnnouncementLevel
  announcementTitle: string
  announcementContent: string
  newCount: number
}

export type AnnouncementLevel = 'notice' | 'success' | 'warning' | 'error'

// How hard the portal asks a reader to refresh after a deploy (internal/app/update_api.go).
//   dismissible  a banner the reader may hide for this target, for this tab session
//   persistent   a banner with no way to hide it that still leaves the page usable
//   required     a closable release-note dialog, followed by a persistent banner
//   automatic    refresh immediately, then confirm the build that loaded
export type UpdatePromptPolicy = 'dismissible' | 'persistent' | 'required' | 'automatic'

// How the home-page "More" button reveals folded quick links.
export type HomeMoreStyle = 'expand' | 'modal' | 'popover'

// Reader-facing placement of the build label. The management console always keeps its own copy.
export type VersionDisplay = 'hidden' | 'footer' | 'header'

// The PUBLIC brand payload from GET /api/site — served with no auth, so the login page can paint
// the right title and logo. Announcements deliberately left it (ADR 0025): a per-audience message
// cannot live on an endpoint anonymous visitors can read and poll. See AnnouncementsProvider.
export interface SiteSettings {
  siteTitle: string
  siteLogoUrl: string
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  versionDisplay: VersionDisplay
  pwaEnabled: boolean
  pwaIconUrl: string
}

// Where an announcement shows. 'home' = the home page only (what the single announcement always
// did); 'app' = every page behind the login.
export type AnnouncementScope = 'home' | 'app'

// Who an announcement is for. 'all' = every signed-in account; 'grant' = only the principals in
// announcement_grants. The reader payload never carries this — see Announcement.
export type AnnouncementAudience = 'all' | 'grant'

// How an announcement is shown to a reader. 'strip' = in the band under the header only; 'popup' =
// a dialog on arrival only; 'both' = the band AND the dialog.
export type AnnouncementDisplay = 'strip' | 'popup' | 'both'

// One announcement as the READER sees it (GET /api/announcements). It carries no audience, no
// grants and no ord on purpose: which OUs are being addressed is the shape of the org chart.
// endsAt is included so a tab left open in the background — where polling stops — can retire an
// expired banner on its own instead of painting it for hours.
export interface Announcement {
  id: number
  level: AnnouncementLevel
  title: string
  content: string
  display: AnnouncementDisplay
  dismissible: boolean
  scope: AnnouncementScope
  endsAt: string
}

// Somebody an announcement (or a report version) can be addressed to: an OU or one account, in the
// single-column `g:<id>` / `u:<name>` encoding the store uses.
export interface Principal {
  principal: string
  name: string
  restricted?: boolean
  display?: string
}

// One announcement as the ADMIN sees it (GET /api/admin/announcements) — the whole row.
export interface AdminAnnouncement extends Announcement {
  ord: number
  enabled: boolean
  audience: AnnouncementAudience
  grants: string[]
  startsAt: string
  createdAt: string
  createdBy: string
  updatedAt: string
}

export interface TokenRow {
  id: number
  prefix: string
  name: string
  scope: string
  created: string
  expires: string
  lastUsed: string
}

// ---- Downloadable iframe apps (docs/adr/0003-downloadable-apps.md) ----

export interface AppSummary {
  id: string
  name: string
  icon?: string
  version?: string
  entry?: string
  scopes?: string[]
}

export interface AppsResp {
  apps: AppSummary[]
}

export interface AppTokenResp {
  app: AppSummary
  token: string
  scopes: string[]
  expires_in: number
}

// One entry in the GitHub-hosted app market index.
export interface AppMarketEntry {
  id: string
  name: string
  icon?: string
  version?: string
  description?: string
  scopes?: string[]
  installed?: boolean
}

export interface AppMarketResp {
  index_url: string
  apps: AppMarketEntry[]
}

// The parse-only response from install?preview=1 (drives the permission prompt).
export interface AppPreviewResp {
  preview: boolean
  app: AppSummary
}

// Recurring tasks (计划任务; docs/adr/0018-recurring-tasks.md): a saved job template + a
// daily/weekly/monthly cadence that the server fires into the run queue.
export interface RecurringTask {
  id: number
  name: string
  target_id: number
  target_name?: string
  concurrency: number
  priority: string // '' (normal) | 'idle'
  max_retries: number
  freq: string // daily | weekly | monthly
  at_time: string // "HH:MM" (panel timezone)
  weekday: number // 0=Sun..6=Sat (weekly)
  monthday: number // 1..31 (monthly)
  enabled: boolean
  created_by: string
  created_at: string
  last_fired: string // YYYY-MM-DD period-stamp of the last fire ("" = never)
  row_count: number
  next_run?: string // computed next fire time (panel tz)
}

// One firing of a task → the batch job it created (the audit/history chain).
export interface RecurringRun {
  id: number
  task_id: number
  job_id: number
  fired_at: string
  job_status?: string
}

// The detail response adds the full template rows + fire history.
export interface RecurringDetail extends RecurringTask {
  rows: Record<string, string>[]
  history: RecurringRun[]
}

export interface RecurringTasksResp {
  tasks: RecurringTask[]
  /**
   * The per-job worker ceiling the server clamps every task's concurrency to (ADR 0001). It rides
   * this list because the recurring form is the only one offering a concurrency picker, and that
   * picker must not offer more than the server will honour.
   */
  maxConcurrency?: number
}

// A login-page SSO button (ADR 0023). Deliberately minimal — the public endpoint exposes no
// issuer, client id or configuration, only what is needed to render and start a sign-in.
export interface SSOProviderInfo {
  slug: string
  kind: 'saml' | 'oidc'
  name: string
  /** Login-button icon: '' | 'preset:<name>' | a /site-assets/ path. Never a remote URL. */
  icon?: string
}

// Admin view of an SSO provider (ADR 0023). Note what is NOT here: no client secret, no SP
// private key — the server reports only whether each is set.
export interface SSOProviderAdmin {
  id: number
  kind: 'saml' | 'oidc'
  slug: string
  name: string
  enabled: boolean
  provisioning: 'off' | 'jit'
  default_group: number
  default_role: string
  default_expiry_days: number
  allow_admin_role: boolean
  session_hours: number
  issuer: string
  client_id: string
  scopes: string
  has_client_secret: boolean
  redirect_url: string // derived from the public URL; read-only
  idp_metadata_url: string
  idp_entity_id: string
  has_idp_metadata: boolean
  allow_idp_initiated: boolean
  clock_skew_sec: number
  icon?: string
  /** How an unlinked login finds an existing account: '' (identity link only) | 'username' | 'email'. */
  link_by?: string
  sp_entity_id: string // derived; paste into the IdP
  sp_acs_url: string // derived; paste into the IdP
  sp_cert_pem: string
  sp_cert_not_after: string
  has_sp_key: boolean
  attr_upn: string
  attr_email: string
  attr_display: string
  attr_groups: string
  attr_external_id: string
}

export interface SSOProvidersResp {
  providers: SSOProviderAdmin[]
  public_url: string
  /**
   * The SP addresses for a provider that has not been saved yet, keyed by kind. They depend only on
   * the public URL and the default slug, and the setup guide needs them before anything is stored.
   */
  sp_defaults?: Record<string, { sp_entity_id?: string; sp_acs_url?: string; redirect_url?: string }>
  /**
   * Whether an SSO flow may reach an RFC1918 address. Off by default: an IdP URL is admin-supplied
   * and points wherever it says, so the portal refuses private targets rather than become a way to
   * probe the host's own network. On for the portal whose IdP genuinely is on the intranet.
   */
  allow_private?: boolean
}

// One group rule. Order in the array is the contract — first match wins — so `ord` and `id` are
// assigned by the server from the submitted order and are never chosen by the client.
export interface SSORuleRow {
  id: number
  provider_id: number // 0 = applies to every provider
  ord: number
  enabled: boolean
  attr: string
  value: string
  target_role: string // '' = leave the role alone
  target_group: number // 0 = leave the OU alone
  keep_on_miss: boolean
  ci: boolean // compare the value case-insensitively
  note: string
}

export interface SSORulesResp {
  rules: SSORuleRow[]
  // Ids of enabled rules an earlier rule already answers for, so they can never win. Computed
  // server-side because the ordering rule lives there.
  shadowed: number[]
}

// ---- Comparing two editions of the same analysis ----

/** One candidate to diff a report against: another edition of the same symbol + subtype. */
export interface ComparableReport {
  id: number
  date: string
  title: string
  version: string
}

export interface DiffLine {
  op: '+' | '-' | ' '
  text: string
}

/** What happened to one section between the two documents. Matched on the documents' own headings,
 *  so the comparison needs no schema and works across every report type. */
export interface SectionDiff {
  heading: string // '' for the text before the first heading
  level: number
  status: 'same' | 'changed' | 'added' | 'removed'
  lines?: DiffLine[] // present for 'changed' only
}

export interface ReportDiff {
  a: { id: number; title: string; date: string; symbol: string; name: string; rtype: string; version: string }
  b: { id: number; title: string; date: string; symbol: string; name: string; rtype: string; version: string }
  sections: SectionDiff[]
  changed: number
}

// ---- The review queue (tracking items) ----

/** One assumption a report rests on, with the report context needed to judge it. */
export interface TrackingRow {
  id: number
  symbol: string
  name: string
  itype: string
  content: string
  status: string
  review_point: string
  /** A date parsed out of review_point when it holds one; '' otherwise. */
  due: string
  created_at: string
  report_id: number
  report_title: string
  report_date: string
  report_kind: string
  report_type: string
}

export interface TrackingResp {
  items: TrackingRow[]
  total: number
  counts: Record<string, number>
  /** The itype and status values actually present — the ingest contract lets a workflow send any
   *  string, so the filters are built from the data rather than from a fixed list. */
  itypes: string[]
  statuses: string[]
}

// ---- Audit log ----

/** A resolved IP address. Every field is optional: a country-level database fills the
 *  first two, and an address the database does not know fills none. */
export interface GeoLocation {
  country_code?: string
  country?: string
  region?: string
  city?: string
  localized_names?: {
    country?: Record<string, string>
    region?: Record<string, string>
    city?: Record<string, string>
  }
}

export interface LoginActivityEntry {
  id: number
  at: string
  ip: string
  geo?: GeoLocation
  method?: string
  client?: ClientSummary
}

export interface ClientSummary {
  browser?: string
  browser_version?: string
  os?: string
  os_version?: string
  device?: string
  device_type?: 'desktop' | 'mobile' | 'tablet' | 'bot' | string
}

export interface LoginActivityResponse {
  items: LoginActivityEntry[]
  total: number
  timezone: string
  keep?: number
}

export interface AccountSession {
  geo?: GeoLocation
  id: string
  created_at: number
  last_seen: number
  expires_at: number
  ip: string
  client?: ClientSummary
  method?: string
  current: boolean
}

export interface AccountSessionsResponse {
  items: AccountSession[]
  timezone: string
}

export interface AuditEntry {
  id: number
  at: string
  actor: string // '' for a machine caller holding a token
  /** The OU the actor was in AT THE TIME — people move, so this is not a live lookup. */
  actor_ou: number
  action: string
  target_type: string
  target_id: string
  detail: string // JSON
  /** Source address; '' for a writer with no request (CLI, scheduler). */
  ip?: string
  /** Where the address resolves to. Resolved server-side at READ time, never stored. */
  geo?: GeoLocation
}

/** The IP-database download's progress. Never carries the URL: it holds a vendor credential. */
/** What IP database is installed and active. */
export interface GeoStatus {
    enabled: boolean
    /** The admin's chosen file; '' = use the newest automatically. */
    pick?: string
    files?: { file: string; modified?: string; ok: boolean; info?: GeoDBInfo }[]
    dir: string
    file?: string
    loaded: boolean
    modified?: string
    info?: GeoDBInfo
}

export interface GeoDBInfo {
  type?: string
  build_epoch?: number
  granularity?: string
}

export interface GeoDBInfo {
  type?: string
  build_epoch?: number
  granularity?: string
}

export interface GeoUpdateState {
  updating: boolean
  last_error?: string
  last_file?: string
  last_at?: string
  /** Whether a credential is stored. The credential itself is never sent back. */
  has_key: boolean
  auto: boolean
  auto_hours: number
  source: string
  edition?: string
  url?: string
}

export interface AuditResp {
  items: AuditEntry[]
  total: number
  /** The action values actually present, so rows written by an older build still filter. */
  actions: string[]
  ou_names: Record<string, string>
  /** The portal's business timezone; '' means follow the reader's. Stamps are UTC. */
  timezone?: string
  /** Whether an IP database is loaded, and where one goes if not. */
  geo?: GeoStatus
  /**
   * True when a forwarded request arrived from a peer that is not in trusted_proxies — meaning
   * every address in the table is the reverse proxy's, identical for every visitor.
   */
  proxy_hint?: boolean
}

/**
 * Live market data for one A-share code (ADR 0028). Everything that is money is an INTEGER
 * number of 分 (fen): the Go schema has no floating-point column anywhere, and a price that may
 * one day be written down must not pass through a float on the way. Divide by 100 to display.
 */
export interface QuoteBar {
  /** Trading date, YYYY-MM-DD, as the vendor dated it — not derived from a clock here. */
  d: string
  o: number
  h: number
  l: number
  c: number
  /** Shares (股), normalised across vendors: Tencent quotes 手, which is 100 shares. */
  v: number
}

export interface QuoteSnapshot {
  last: number
  prevClose: number
  open: number
  high: number
  low: number
  /** Signed, in 分. The UI takes the up/down sign from HERE, never from changePct. */
  change: number
  /**
   * The vendor's own percentage string, e.g. "0.12" — rendered verbatim, never recomputed.
   * Recomputing it from last/prevClose prints a fake double-digit crash on every ex-rights
   * day, because the previous close is the pre-dividend one and the last price is not.
   */
  changePct: string
  volume: number
  amount: number
  /** The vendor's own timestamp (RFC3339, +08:00), so a stale feed is visible as stale. */
  asOf: string
  session: 'open' | 'close' | 'unknown'
}

export interface QuoteResp {
  symbol: string
  /** The live name from the quote feed — not the name frozen onto a report at ingest. */
  name: string
  /**
   * Which exchange answered. Quote viewing covers more markets than the portal analyses: reports
   * are A-share only, so a US or HK instrument has a price here and no reports anywhere.
   */
  market: 'sh' | 'sz' | 'bj' | 'hk' | 'us'
  /**
   * An index has no turnover worth showing and is never a report subject.
   *
   * Empty is a real value, not a gap: the Sina fallback's line carries no instrument-type column at
   * all -- an index comes down it in the same 34-field shape a stock does -- so that path reports
   * what it read rather than guessing "stock". Narrow this to two values and the next `kind ===
   * 'stock' ? … : …` renders every failover response as an index, complete with the 指数 tag on an
   * ordinary stock, precisely while the portal is already degraded.
   */
  kind: 'stock' | 'index' | ''
  /**
   * The currency the prices are in. Not cosmetic: a USD price rendered as a bare number beside a
   * CNY one reads as the same kind of quantity, which is how a reader mis-compares them.
   */
  currency: 'CNY' | 'HKD' | 'USD'
  /**
   * The IANA zone `asOf` belongs to. A US close stamped +08:00 lands fourteen hours in the future
   * for a reader in China, which looks like a live quote rather than last night's.
   */
  tz: string
  source: 'tencent' | 'sina'
  snapshot: QuoteSnapshot
  /** Oldest first. Always an array, never null. Empty when barsUnavailable is set. */
  bars: QuoteBar[]
  barsSource: string
  /**
   * Why there is no history, when there is none. Three answers, and the UI must say which rather
   * than draw an empty chart that reads as a flat one:
   *
   * - 'market_unsupported' — a DAILY range on a market whose daily series is not worth drawing
   *   (Beijing, and the US with nothing but the shipped sources). A standing gap: nothing an
   *   operator enables changes it.
   * - 'interval_unsupported' — a window no ENABLED source declares. The market has the data and
   *   this deployment has no source for it, so the fix is an operator's, in 管理 → 行情源. These
   *   two were one code, which is how enabling a source for a window left the same "this market
   *   has no data" sentence on screen and looked like it had done nothing.
   * - 'source_failed' — every source that could have answered did not. Worth retrying; the other
   *   two are not.
   */
  barsUnavailable: '' | 'market_unsupported' | 'interval_unsupported' | 'source_failed'
  /** Always false: we serve 不复权 (bfq) prices, whose value for a past date never changes. */
  adjusted: boolean
  cached: boolean
  /** Server-owned advice for the next visible refresh. Omitted when automatic refresh is off. */
  refreshAfterSecs?: number
  /** Absolute market-zone wake time used after close. Mutually exclusive with refreshAfterSecs. */
  refreshAt?: string
}

// ── Forecast verification ────────────────────────────────────────────────────────────────────────
//
// The ledger's read shapes (ADR 0029 §5, plan §4.1) and the group statistics (plan §7.6). Prices are
// integer 分 on the wire; only the display layer converts, for the same reason the quote contract
// does. A field the server omits is genuinely unknown, so most metric fields are optional: a
// non-mature cell carries NO metric keys, and `undefined` is how the UI tells that apart from a
// measured zero.

export type PredictionState = 'absent' | 'rejected' | 'backtest' | 'accepted' | 'test'
export type ForecastCheckpoint = 'd5' | 'd20' | 'd60' | 'd120' | 'h12'

export interface PredictionClaim {
  key: string
  kind: 'stance' | 'terminal_value' | 'scenario' | 'entry_price' | 'stop_loss'
  stance?: 'long' | 'no_position'
  label?: 'bull' | 'base' | 'bear'
  price_fen?: number
  prob_bp?: number
  weight_bp?: number
}

export interface PredictionMark {
  checkpoint: ForecastCheckpoint
  status: 'pending' | 'resolved' | 'refused' | 'refused_final'
  refusal?: string
  session_date?: string
  bar_date?: string
  halted?: boolean
  corp_action?: boolean
  close_fen?: number
  low_fen?: number
  high_fen?: number
  ret_bp?: number
  bench_ret_bp?: number
  attempts?: number
  source?: string
  direction?: 'agree' | 'disagree' | 'flat' | 'untested'
  /** Derived reading-page state; retrying requires a recorded due session. */
  phase?: 'waiting_s0' | 'waiting' | 'settling' | 'retrying' | 'overdue' | 'resolved' | 'final'
  /** The earliest date the mark can settle; a projection is only a lower bound. */
  not_before?: string
  /** Based on a published exchange closure schedule, not an observed settlement date. */
  expected?: boolean
}

export interface PredictionSetError {
  reason: string
  path: string
  message: string
}

export type ForecastMembership =
  | 'member' | 'duplicate' | 'degraded' | 'mismatch' | 'pending_baseline'
  | 'pre_live' | 'off_benchmark' | 'test' | 'backtest' | 'absent' | 'rejected' | 'unknown'

export interface PredictionSet {
  id: number
  report_id?: number
  state: PredictionState
  symbol: string
  rtype: string
  title: string
  version: string
  /** Optional during mixed-version rollout; newer servers always return the stored report date. */
  report_date?: string
  /** Global monthly membership, omitted for protected readers and when classification fails. */
  membership?: ForecastMembership
  /** Integer basis points, truncated toward zero; absent without a usable base and target. */
  upside_bp?: number
  made_at: string
  made_on: string
  cohort: string
  zone: string
  bench_code: string
  decision_status?: string
  decision_quality?: string
  probability_method?: string
  policy_version?: string
  horizon_end?: string
  horizon_std: boolean
  baseline_emitted_fen?: number
  baseline_as_of?: string
  baseline_source?: string
  baseline_check: 'pending' | 'match' | 'mismatch' | 'refused' | 'refused_final'
  s0_date?: string
  s0_fen?: number
  /** True when the report's body changed after this set was written. */
  body_changed: boolean
  errors?: PredictionSetError[]
  claims: PredictionClaim[]
  marks: PredictionMark[]
  payload?: string
}

export interface ReportPredictions {
  ok: boolean
  sets: PredictionSet[]
  counted_set_id?: number
  display_set_id?: number
  settlement_paused?: boolean
}

export type ForecastMode = 'month' | 'custom'
export type ForecastGrouping = 'week' | 'month' | 'quarter' | 'all'
export type ForecastMetric = 'spread' | 'ic' | 'hit' | 'fill'

export interface ForecastCoverage {
  reports: number
  member: number
  duplicate: number
  absent: number
  rejected: number
  backtest: number
  degraded: number
  mismatch: number
  pending_baseline: number
  pre_live: number
  test: number
  off_benchmark: number
  unknown?: number
}

export interface ForecastCell {
  state: 'open' | 'pending' | 'small' | 'mature' | 'purged'
  pending_reasons?: Partial<Record<NonNullable<PredictionMark['phase']> | 'unknown', number>>
  failed_cap_exceeded?: boolean
  not_before?: string
  expected?: boolean
  n?: number
  resolved?: number
  total?: number
  refused?: number
  corp_action?: number

  spread_bp?: number
  spread_one_arm?: boolean
  spread_p?: number
  spread_q?: number

  ic?: number
  ic_n?: number
  ic_p?: number
  ic_q?: number

  hit?: number
  hit_n?: number
  hit_null?: number
  hit_p?: number
  hit_q?: number

  fill?: number
  fill_ret_bp?: number
  fill_derived?: boolean

  band?: number
  band_n?: number
  calib?: number
  calib_n?: number

  slices?: ForecastSlice[]
}

export interface ForecastSlice extends ForecastCell {
  policy_version: string
}

export interface ForecastGroup {
  label: string
  start: string
  end: string
  official: boolean
  purged_at?: string
  /** The index every member was frozen with (P4); a split custom range has one group per index. */
  bench: string
  coverage: ForecastCoverage
  cells: Record<ForecastCheckpoint, ForecastCell>
}

export interface ForecastGroupsResponse {
  ok: boolean
  mode: ForecastMode
  official: boolean
  range: { from: string; to: string; group?: ForecastGrouping }
  checkpoints: ForecastCheckpoint[]
  unrecorded: number
  groups: ForecastGroup[]
  excluded_months?: ForecastPurgedMonth[]
}

export interface ForecastPurgedMonth {
  cohort: string
  purged_at: string
}

export interface ForecastMember {
  set_id: number
  report_id?: number
  symbol: string
  name?: string
  kind?: string
  report_date: string
  made_at: string
  made_on: string
  stance: 'long' | 'no_position'
  status: string
  excluded_reason?: 'pending_baseline' | 'non_standard_horizon' | 'missing_mark' | 'pending' | 'refused' | 'refused_final' | 'corp_action' | 'unknown_mark_status'
  close_fen?: number
  ret_bp?: number
  bench_ret_bp?: number
  excess_bp?: number
  s0_fen?: number
  terminal_fen?: number
  upside?: number
  direction?: 'agree' | 'disagree' | 'flat' | 'untested'
}

export interface ForecastMembersResponse {
  ok: boolean
  label: string
  start: string
  end: string
  checkpoint: ForecastCheckpoint
  state: string
  population?: 'scored' | 'all'
  members: ForecastMember[]
  purged_at?: string
  excluded_months?: ForecastPurgedMonth[]
}
