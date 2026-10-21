package app

// These additions follow the owner's approved Option A. The accepted baseline
// stays unchanged; the migration ledger and backup enumeration own the additions.
var notificationTables = []string{
	"notification_events", "notification_channels", "notification_routes",
	"notification_route_grants", "notification_deliveries", "notification_attempts",
	"notification_recipients", "notification_preferences", "job_notification_intents",
	"notification_source_cycles", "notification_config_mappings",
}

func migration0007() migration {
	return migration{id: "0007", tables: notificationTables,
		indices: []string{"idx_notification_due", "idx_notification_inbox", "idx_notification_attempts", "idx_notification_events_time"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for _, q := range []string{
				`CREATE TABLE IF NOT EXISTS notification_events (
					id TEXT PRIMARY KEY, source_key TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
					payload TEXT NOT NULL, resource_type TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
					created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL DEFAULT 0)`,
				`CREATE TABLE IF NOT EXISTS notification_channels (
					id TEXT PRIMARY KEY, label TEXT NOT NULL, adapter TEXT NOT NULL,
					legacy_ref TEXT NOT NULL DEFAULT '', config TEXT NOT NULL DEFAULT '{}', secret TEXT NOT NULL DEFAULT '',
					enabled INTEGER NOT NULL DEFAULT 1, paused INTEGER NOT NULL DEFAULT 0,
					revision BIGINT NOT NULL DEFAULT 1, created_at BIGINT NOT NULL)`,
				`CREATE TABLE IF NOT EXISTS notification_routes (
					id TEXT PRIMARY KEY, label TEXT NOT NULL, channel_id TEXT NOT NULL,
					events TEXT NOT NULL, recipient_mode TEXT NOT NULL DEFAULT 'grant',
					enabled INTEGER NOT NULL DEFAULT 1, mandatory INTEGER NOT NULL DEFAULT 0,
					revision BIGINT NOT NULL DEFAULT 1)`,
				`CREATE TABLE IF NOT EXISTS notification_route_grants (
					route_id TEXT NOT NULL, principal TEXT NOT NULL, PRIMARY KEY(route_id,principal))`,
				`CREATE TABLE IF NOT EXISTS notification_deliveries (
					id TEXT PRIMARY KEY, event_id TEXT NOT NULL, channel_id TEXT NOT NULL,
					route_id TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
					state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
					next_at BIGINT NOT NULL DEFAULT 0, expires_at BIGINT NOT NULL,
					lease_token TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0,
					sending INTEGER NOT NULL DEFAULT 0, channel_revision BIGINT NOT NULL DEFAULT 0,
					route_revision BIGINT NOT NULL DEFAULT 0, generation INTEGER NOT NULL DEFAULT 0,
					replay_of TEXT NOT NULL DEFAULT '', replay_key TEXT UNIQUE,
					last_error TEXT NOT NULL DEFAULT '', status_code INTEGER NOT NULL DEFAULT 0,
					created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL DEFAULT 0,
					UNIQUE(event_id,channel_id,route_id,username,generation))`,
				`CREATE TABLE IF NOT EXISTS notification_attempts (
					id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL, lease_token TEXT NOT NULL,
					started_at BIGINT NOT NULL, finished_at BIGINT NOT NULL DEFAULT 0,
					outcome TEXT NOT NULL DEFAULT 'sending', error_code TEXT NOT NULL DEFAULT '',
					status_code INTEGER NOT NULL DEFAULT 0)`,
				`CREATE TABLE IF NOT EXISTS notification_recipients (
					id TEXT PRIMARY KEY, event_id TEXT NOT NULL, username TEXT NOT NULL,
					read_at BIGINT NOT NULL DEFAULT 0, dismissed_at BIGINT NOT NULL DEFAULT 0,
					UNIQUE(event_id,username))`,
				`CREATE TABLE IF NOT EXISTS notification_preferences (
					username TEXT NOT NULL, event_type TEXT NOT NULL,
					enabled INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(username,event_type))`,
				`CREATE TABLE IF NOT EXISTS job_notification_intents (
					job_id BIGINT PRIMARY KEY, username TEXT NOT NULL, created_at BIGINT NOT NULL,
					consumed_at BIGINT NOT NULL DEFAULT 0)`,
				`CREATE TABLE IF NOT EXISTS notification_source_cycles (
					source_key TEXT PRIMARY KEY, generation BIGINT NOT NULL DEFAULT 0,
					occurrence_id TEXT NOT NULL, signature TEXT NOT NULL DEFAULT '')`,
				`CREATE TABLE IF NOT EXISTS notification_config_mappings (
					legacy_key TEXT PRIMARY KEY, channel_id TEXT NOT NULL)`,
				`CREATE INDEX IF NOT EXISTS idx_notification_due ON notification_deliveries(state,next_at,lease_until,created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_notification_inbox ON notification_recipients(username,dismissed_at,read_at,event_id)`,
				`CREATE INDEX IF NOT EXISTS idx_notification_attempts ON notification_attempts(delivery_id,started_at)`,
				`CREATE INDEX IF NOT EXISTS idx_notification_events_time ON notification_events(created_at,id)`,
			} {
				if _, err := e.exec(q); err != nil {
					return err
				}
			}
			return nil
		}}
}
