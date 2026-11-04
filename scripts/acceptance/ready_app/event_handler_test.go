package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func fixtureDelivery(eventID string, occurred int64) appplatform.EventDelivery {
	return appplatform.EventDelivery{
		Format:       appplatform.EventDeliveryFormat,
		Subscription: "20000000-0000-4000-8000-000000000001",
		EventID:      eventID, EventType: appplatform.EventReportIngested,
		ResourceType: "report", ResourceID: "41",
		OccurredMS: "1000", Sequence: "1000," + eventID,
	}
}

func TestFixtureEventHandlerDeduplicatesAndOrdersDurably(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events.json")
	handler, err := newFixtureEventHandler(path, root)
	if err != nil {
		t.Fatal(err)
	}
	newer := fixtureDelivery("10000000-0000-4000-8000-000000000102", 2000)
	newer.OccurredMS = "2000"
	newer.Sequence = "2000," + newer.EventID
	if decision, drop, err := handler.apply(newer, []byte(`{"newer":true}`)); err != nil ||
		decision != "applied" || drop {
		t.Fatal("newer event", decision, drop, err)
	}
	if decision, _, err := handler.apply(newer, []byte(`{"newer":true}`)); err != nil ||
		decision != "duplicate" {
		t.Fatal("duplicate event", decision, err)
	}
	if _, _, err := handler.apply(newer, []byte(`{"changed":true}`)); !errors.Is(
		err, errFixtureEventIdentity) {
		t.Fatal("changed duplicate accepted", err)
	}
	older := fixtureDelivery("10000000-0000-4000-8000-000000000101", 1000)
	if decision, _, err := handler.apply(older, []byte(`{"older":true}`)); err != nil ||
		decision != "ignored_older" {
		t.Fatal("older event", decision, err)
	}
	reloaded, err := newFixtureEventHandler(path, root)
	if err != nil {
		t.Fatal(err)
	}
	if decision, _, err := reloaded.apply(newer, []byte(`{"newer":true}`)); err != nil ||
		decision != "duplicate" {
		t.Fatal("persisted duplicate", decision, err)
	}
}

func TestFixtureEventHandlerPersistsBeforeDroppingResponse(t *testing.T) {
	root := t.TempDir()
	handler, err := newFixtureEventHandler(filepath.Join(root, "events.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	delivery := fixtureDelivery("10000000-0000-4000-8000-000000000103", 1000)
	body := []byte(`{"drop_once":true}`)
	delivery.Payload = body
	if decision, drop, err := handler.apply(delivery, body); err != nil ||
		decision != "applied" || !drop {
		t.Fatal("first event", decision, drop, err)
	}
	reloaded, err := newFixtureEventHandler(filepath.Join(root, "events.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	if decision, drop, err := reloaded.apply(delivery, body); err != nil ||
		decision != "duplicate" || drop {
		t.Fatal("lost acknowledgement replay", decision, drop, err)
	}
}
