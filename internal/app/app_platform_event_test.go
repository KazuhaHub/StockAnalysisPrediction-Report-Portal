package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestPlatformEventSubscriptionAdmissionAndAuthority(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	spec := appplatform.EventSubscriptionSpec{
		EventType: appplatform.EventReportIngested, ResourceType: "report",
		ResourceID: "41", MaxInflight: 4,
	}
	id, created, err := s.createPlatformEventSubscription(
		context.Background(), installation, spec)
	if err != nil || !created || !platformValidUUID(id) {
		t.Fatal("create event subscription", id, created, err)
	}
	again, created, err := s.createPlatformEventSubscription(
		context.Background(), installation, spec)
	if err != nil || created || again != id {
		t.Fatal("idempotent event subscription", again, created, err)
	}
	if _, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: "extension.arbitrary.notice", ResourceType: "extension",
			ResourceID: "41", MaxInflight: 1,
		}); !errors.Is(err, errAppPlatformEventUnavailable) {
		t.Fatal("unknown event type accepted", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants
		SET granted=0 WHERE installation_id=? AND capability='events.subscribe'`,
		installation)
	if _, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "42", MaxInflight: 1,
		}); !errors.Is(err, errAppPlatformEventUnavailable) {
		t.Fatal("revoked events.subscribe remained usable", err)
	}
}

func TestPlatformEventSubscriptionInstallationQuota(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	for i := 1; i <= 16; i++ {
		if _, _, err := s.createPlatformEventSubscription(
			context.Background(), installation,
			appplatform.EventSubscriptionSpec{
				EventType: appplatform.EventReportIngested, ResourceType: "report",
				ResourceID: strconv.Itoa(i), MaxInflight: 1,
			}); err != nil {
			t.Fatalf("create subscription %d: %v", i, err)
		}
	}
	if _, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "17", MaxInflight: 1,
		}); !errors.Is(err, errAppPlatformEventQuota) {
		t.Fatal("installation event subscription quota was not enforced", err)
	}
	if got := scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_event_subscriptions`); got != 16 {
		t.Fatalf("event subscriptions = %d", got)
	}
}

func TestPlatformEventCaptureAndDeliveryAreTransactional(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
			id, created, err := s.createPlatformEventSubscription(
				context.Background(), installation,
				appplatform.EventSubscriptionSpec{
					EventType:    appplatform.EventReportIngested,
					ResourceType: "report", ResourceID: "41", MaxInflight: 4,
				})
			if err != nil || !created {
				t.Fatal("subscription", id, created, err)
			}
			eventID := "10000000-0000-4000-8000-000000000041"
			event := NotificationEvent{
				ID: eventID, SourceKey: "report:41:event",
				Type: appplatform.EventReportIngested, Payload: `{"id":41}`,
				ResourceType: "report", ResourceID: "41",
				CreatedAt: time.Now().UTC().UnixMilli(),
			}
			if err := s.st.CaptureNotification(event, nil, nil); err != nil {
				t.Fatal(err)
			}
			tasks := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks
				WHERE handler='app.event.delivery' AND installation_id=?`,
				installation)
			if tasks != 1 {
				t.Fatalf("event did not create one delivery task: tasks=%d subscription=%s",
					tasks, scalar[string](t, s.st,
						`SELECT state||':'||revision FROM app_platform_event_subscriptions WHERE id=?`, id))
			}
			var payload []byte
			if err := s.st.queryRow(`SELECT payload FROM app_platform_tasks
				WHERE handler='app.event.delivery'`).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			delivery, err := appplatform.ParseEventDelivery(payload)
			if err != nil || delivery.Subscription != id ||
				delivery.EventID != eventID ||
				delivery.ResourceID != "41" {
				t.Fatal("delivery payload", delivery, err)
			}
			if err := s.st.CaptureNotification(event, nil, nil); err != nil {
				t.Fatal("duplicate source capture", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_tasks
				WHERE handler='app.event.delivery'`) != 1 {
				t.Fatal("source replay created another task")
			}
			changed := event
			changed.Payload = `{"id":41,"changed":true}`
			if err := s.st.CaptureNotification(changed, nil, nil); !errors.Is(err, errNotificationConflict) {
				t.Fatal("changed source bytes did not conflict", err)
			}
		})
	}
}

func TestPlatformEventSubscriptionLifecycleAndRestore(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	id, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventBatchFinished, ResourceType: "job",
			ResourceID: "7@1", MaxInflight: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelPlatformEventSubscriptionsForInstallation(
		s.st.poolExec(), installation, "paused", now); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state FROM app_platform_event_subscriptions WHERE id=?`, id); got != "paused" {
		t.Fatalf("paused state = %q", got)
	}
	if err := s.rebindPlatformEventSubscription(
		context.Background(), id, installation, 2); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state||':'||revision FROM app_platform_event_subscriptions WHERE id=?`,
		id); got != "active:3" {
		t.Fatalf("rebound state = %q", got)
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(
		strings.NewReader(string(dumpOf(t, s.st))), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, dst,
		`SELECT state FROM app_platform_event_subscriptions WHERE id=?`, id); got != "review_required" {
		t.Fatalf("restored state = %q", got)
	}
}

func TestPlatformEventBatchDeliveryMatchesOwnerScopedJob(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	id, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventBatchFinished, ResourceType: "job",
			ResourceID: "7@3", MaxInflight: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	event := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000061",
		SourceKey: "job-completion:owner-scoped", Type: appplatform.EventBatchFinished,
		Payload: `{"job_id":7,"status":"finished"}`, ResourceType: "job",
		ResourceID: "7@3", CreatedAt: time.Now().UTC().UnixMilli(),
	}
	if err := s.st.CaptureNotification(event, nil, nil); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.st.queryRow(`SELECT payload FROM app_platform_tasks
		WHERE installation_id=? AND handler=?`,
		installation, appplatform.TaskHandlerEvent).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	delivery, err := appplatform.ParseEventDelivery(payload)
	if err != nil || delivery.Subscription != id ||
		delivery.ResourceID != "7@3" {
		t.Fatal("owner-scoped job delivery", delivery, err)
	}
}

func TestPlatformEventSubscriptionRevocationIsMonotonic(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	id, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.transitionPlatformEventSubscription(
		context.Background(), id, installation, "revoked", 1); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelPlatformEventSubscriptionsForInstallation(
		s.st.poolExec(), installation, "review_required", now); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state FROM app_platform_event_subscriptions WHERE id=?`, id); got != "revoked" {
		t.Fatalf("revoked subscription changed to %q", got)
	}
	if err := s.rebindPlatformEventSubscription(
		context.Background(), id, installation, 2); !errors.Is(
		err, errAppPlatformEventConflict) {
		t.Fatal("revoked subscription was rebound", err)
	}
}

func TestPlatformEventCaptureRollsBackWhenTaskIdentityConflicts(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	subscriptionID, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 2,
		})
	if err != nil {
		t.Fatal(err)
	}
	eventID := "10000000-0000-4000-8000-000000000042"
	occurred := time.Now().UTC().UnixMilli()
	occurrence, err := appplatform.EventOccurrence(eventID, 1)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := appplatform.BuildEventDelivery(
		subscriptionID, eventID, appplatform.EventReportIngested,
		"report", "41", occurred, []byte(`{"id":99}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.admitPlatformTask(
		context.Background(), installation, platformTaskAdmission{
			OccurrenceID: occurrence, Handler: appplatform.TaskHandlerEvent,
			Payload: payload, ExpiresMS: occurred + 60000,
		}); err != nil || !created {
		t.Fatal("seed conflicting task", created, err)
	}
	event := NotificationEvent{
		ID: eventID, SourceKey: "report:41:rollback",
		Type: appplatform.EventReportIngested, Payload: `{"id":41}`,
		ResourceType: "report", ResourceID: "41", CreatedAt: occurred,
	}
	if err := s.st.CaptureNotification(event, nil, nil); !errors.Is(err, errNotificationConflict) {
		t.Fatal("conflicting task did not roll back event capture", err)
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM notification_events WHERE source_key=?`,
		event.SourceKey) != 0 {
		t.Fatal("failed event task capture left notification event behind")
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_tasks WHERE occurrence_id=?`,
		occurrence) != 1 {
		t.Fatal("task conflict left duplicate task identities")
	}
}

func TestPlatformEventSubscriptionAdminAPI(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	create := httptest.NewRecorder()
	body := `{"installationId":"` + installation +
		`","eventType":"report.ingested","resourceType":"report",` +
		`"resourceId":"41","maxInflight":4}`
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/app-platform/event-subscriptions", strings.NewReader(body))
	s.apiPlatformEventSubscriptionCreate(create, req, "admin")
	if create.Code != http.StatusOK {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(create.Body.Bytes(), &created) != nil ||
		!platformValidUUID(created.ID) {
		t.Fatal("create response", create.Body.String())
	}
	list := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet,
		"/api/admin/app-platform/event-subscriptions?installation="+
			installation, nil)
	s.apiPlatformEventSubscriptionList(list, listReq, "admin")
	if list.Code != http.StatusOK ||
		!strings.Contains(list.Body.String(), created.ID) {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	pause := httptest.NewRecorder()
	pauseReq := httptest.NewRequest(http.MethodPost,
		"/api/admin/app-platform/event-subscriptions/"+created.ID+"/state?installation="+installation,
		strings.NewReader(`{"state":"paused","expectedRevision":1}`))
	pauseReq.SetPathValue("id", created.ID)
	s.apiPlatformEventSubscriptionState(pause, pauseReq, "admin")
	if pause.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", pause.Code, pause.Body.String())
	}
	rebind := httptest.NewRecorder()
	rebindReq := httptest.NewRequest(http.MethodPost,
		"/api/admin/app-platform/event-subscriptions/"+created.ID+"/rebind?installation="+installation,
		strings.NewReader(`{"expectedRevision":2}`))
	rebindReq.SetPathValue("id", created.ID)
	s.apiPlatformEventSubscriptionRebind(rebind, rebindReq, "admin")
	if rebind.Code != http.StatusOK {
		t.Fatalf("rebind: %d %s", rebind.Code, rebind.Body.String())
	}
}

func TestPlatformEventSubscriptionInflightBoundIsAtomic(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	id, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	first := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000051",
		SourceKey: "report:41:capacity-1", Type: appplatform.EventReportIngested,
		Payload: `{"id":41}`, ResourceType: "report", ResourceID: "41",
		CreatedAt: time.Now().UTC().UnixMilli(),
	}
	if err := s.st.CaptureNotification(first, nil, nil); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "10000000-0000-4000-8000-000000000052"
	second.SourceKey = "report:41:capacity-2"
	if err := s.st.CaptureNotification(second, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state FROM app_platform_event_subscriptions WHERE id=?`,
		id); got != "review_required" {
		t.Fatalf("saturated subscription state = %q", got)
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_tasks WHERE handler=?`,
		appplatform.TaskHandlerEvent) != 1 {
		t.Fatal("saturated subscription admitted another delivery")
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM notification_events WHERE source_key=?`,
		second.SourceKey) != 1 {
		t.Fatal("event subscription saturation discarded the source event")
	}
}

func TestPlatformEventInflightIsScopedPerSubscription(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	for _, resourceID := range []string{"41", "42"} {
		if _, _, err := s.createPlatformEventSubscription(
			context.Background(), installation,
			appplatform.EventSubscriptionSpec{
				EventType: appplatform.EventReportIngested, ResourceType: "report",
				ResourceID: resourceID, MaxInflight: 1,
			}); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []NotificationEvent{
		{
			ID:        "10000000-0000-4000-8000-000000000071",
			SourceKey: "report:41:separate-capacity", Type: appplatform.EventReportIngested,
			Payload: `{"id":41}`, ResourceType: "report", ResourceID: "41",
			CreatedAt: time.Now().UTC().UnixMilli(),
		},
		{
			ID:        "10000000-0000-4000-8000-000000000072",
			SourceKey: "report:42:separate-capacity", Type: appplatform.EventReportIngested,
			Payload: `{"id":42}`, ResourceType: "report", ResourceID: "42",
			CreatedAt: time.Now().UTC().UnixMilli(),
		},
	} {
		if err := s.st.CaptureNotification(event, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_tasks WHERE handler=?`,
		appplatform.TaskHandlerEvent); got != 2 {
		t.Fatalf("per-subscription delivery tasks = %d", got)
	}
	if got := scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_event_subscriptions
		WHERE state='active'`); got != 2 {
		t.Fatalf("active subscriptions = %d", got)
	}
}

func TestPlatformEventExpiredSourceCreatesNoDelivery(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	if _, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 1,
		}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	event := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000073",
		SourceKey: "report:41:expired", Type: appplatform.EventReportIngested,
		Payload: `{"id":41}`, ResourceType: "report", ResourceID: "41",
		CreatedAt: now - 2000, ExpiresAt: now - 1000,
	}
	if err := s.st.CaptureNotification(event, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_tasks WHERE handler=?`,
		appplatform.TaskHandlerEvent); got != 0 {
		t.Fatalf("expired source admitted %d deliveries", got)
	}
}

func TestPlatformEventSubscriptionRevocationFencesClaimedDelivery(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	id, _, err := s.createPlatformEventSubscription(
		context.Background(), installation,
		appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report",
			ResourceID: "41", MaxInflight: 1,
		})
	if err != nil {
		t.Fatal(err)
	}
	event := NotificationEvent{
		ID:        "10000000-0000-4000-8000-000000000044",
		SourceKey: "report:41:fence", Type: appplatform.EventReportIngested,
		Payload: `{"id":41}`, ResourceType: "report", ResourceID: "41",
		CreatedAt: time.Now().UTC().UnixMilli(),
	}
	if err := s.st.CaptureNotification(event, nil, nil); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.claimNextPlatformTask(
		context.Background(), platformFixtureID(7401))
	if err != nil || claimed.ID == "" {
		t.Fatal("claim", claimed, err)
	}
	started, identity, err := s.startPlatformTask(
		context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.transitionPlatformEventSubscription(
		context.Background(), id, installation, "paused", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.completePlatformTask(
		context.Background(), started, identity,
		appplatform.TaskExecutionResult{
			Outcome:    appplatform.TaskResultSucceeded,
			ResultJSON: []byte(`{"private":"withheld"}`),
		}); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st,
		`SELECT state||':'||error_code FROM app_platform_tasks WHERE id=?`,
		started.ID); got != "uncertain:authority_revoked" {
		t.Fatalf("fenced task = %q", got)
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM app_platform_tasks WHERE result_json IS NOT NULL`) != 0 {
		t.Fatal("revoked event subscription released result")
	}
}
