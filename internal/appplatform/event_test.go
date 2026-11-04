package appplatform

import (
	"bytes"
	"errors"
	"testing"
)

func TestEventSubscriptionCatalogueIsClosed(t *testing.T) {
	for _, spec := range []EventSubscriptionSpec{
		{EventReportIngested, "report", "42", 4},
		{EventBatchFinished, "job", "7", 1},
	} {
		if ValidateEventSubscription(spec) != nil {
			t.Fatal("valid subscription refused", spec)
		}
	}
	for _, spec := range []EventSubscriptionSpec{
		{"extension.arbitrary.notice", "extension", "42", 1},
		{EventReportIngested, "job", "42", 1},
		{EventBatchFinished, "job", "0", 1},
		{EventReportIngested, "report", "042", 1},
		{EventBatchFinished, "job", "7", 17},
	} {
		if !errors.Is(ValidateEventSubscription(spec), ErrEventContract) {
			t.Fatal("invalid subscription accepted", spec)
		}
	}
}

func TestEventDeliveryIsBoundedCanonicalAndOrdered(t *testing.T) {
	eventID := "10000000-0000-4000-8000-000000000001"
	raw, sequence, err := BuildEventDelivery(
		eventID, EventReportIngested, "report", "42", 1000,
		[]byte(`{"title":"Changed","id":42}`))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := ParseEventDelivery(raw)
	if err != nil || delivery.Sequence != sequence ||
		!bytes.Contains(delivery.Payload, []byte(`"id":42`)) {
		t.Fatal("delivery", delivery, err)
	}
	later, _, err := BuildEventDelivery(
		"10000000-0000-4000-8000-000000000002",
		EventReportIngested, "report", "42", 1000, []byte(`{"id":42}`))
	laterDelivery, laterErr := ParseEventDelivery(later)
	order := EventDeliveryOrder(delivery, laterDelivery)
	if err != nil || laterErr != nil || order >= 0 {
		t.Fatal("same-instant event IDs are not a stable sequence")
	}
	for _, bad := range [][]byte{
		nil,
		append(append([]byte(nil), raw...), []byte("{}")...),
		bytes.Replace(raw, []byte(`"report"`), []byte(`"global"`), 1),
		bytes.Replace(raw, []byte(`"occurred_ms":"1000"`), []byte(`"occurred_ms":"01"`), 1),
		bytes.Replace(raw, []byte(`"sequence":"1000,`+eventID+`"`), []byte(`"sequence":"999,`+eventID+`"`), 1),
		bytes.Replace(raw, []byte(`"payload":{"id":42,"title":"Changed"}`), []byte(`"payload":{"title":"Changed","id":42}`), 1),
	} {
		if _, err := ParseEventDelivery(bad); !errors.Is(err, ErrEventContract) {
			t.Fatal("invalid event delivery accepted")
		}
	}
	occurrence, err := EventOccurrence(eventID, 2)
	if err != nil || occurrence != "event:"+eventID+":2" {
		t.Fatal("occurrence", occurrence, err)
	}
}
