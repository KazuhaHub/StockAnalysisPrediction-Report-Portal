export const DASHBOARD_METRICS: Record<string, { key: string; label: string }[]> = {
  reports: [
    { key: 'total', label: 'totalReports' }, { key: 'today', label: 'todayReports' },
    { key: 'yesterday', label: 'yesterdayReports' }, { key: 'recent', label: 'recentReports' },
    { key: 'month', label: 'monthReports' }, { key: 'year', label: 'yearReports' },
  ],
  queue: [
    { key: 'running_rows', label: 'running' }, { key: 'running', label: 'runningJobs' },
    { key: 'waiting', label: 'waiting' }, { key: 'scheduled', label: 'scheduled' },
    { key: 'budget', label: 'capacity' }, { key: 'reserved', label: 'reserved' }, { key: 'done_today', label: 'doneToday' },
  ],
  forecast: [
    { key: 'due_sets', label: 'due' }, { key: 'pending', label: 'pending' },
    { key: 'refused', label: 'refused' }, { key: 'unsettled_baseline', label: 'unsettledBaseline' },
  ],
}
export const DEFAULT_METRICS: Record<string, string[]> = {
  reports: ['total', 'today', 'recent'], queue: ['running_rows', 'waiting', 'scheduled'], forecast: ['due_sets', 'pending'],
}
