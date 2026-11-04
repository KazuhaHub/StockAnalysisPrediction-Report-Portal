package appplatform

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

var ErrEventContract = errors.New("application event contract unavailable")

const (
	EventReportIngested = "report.ingested"
	EventBatchFinished  = "batch.job.finished"
	EventDeliveryFormat = "portal.app-event.delivery/1-dev.1"
)

type EventSubscriptionSpec struct {
	EventType    string
	ResourceType string
	ResourceID   string
	MaxInflight  uint64
}

func ValidateEventSubscription(spec EventSubscriptionSpec) error {
	if spec.MaxInflight < 1 || spec.MaxInflight > 16 ||
		len(spec.ResourceID) < 1 || len(spec.ResourceID) > 256 ||
		!eventResourceID(spec.ResourceID) {
		return ErrEventContract
	}
	switch spec.EventType {
	case EventReportIngested:
		if spec.ResourceType != "report" {
			return ErrEventContract
		}
	case EventBatchFinished:
		if spec.ResourceType != "job" {
			return ErrEventContract
		}
	default:
		return ErrEventContract
	}
	return nil
}

func eventResourceID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

type EventDelivery struct {
	Format       string          `json:"format"`
	EventID      string          `json:"event_id"`
	EventType    string          `json:"event_type"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	OccurredMS   string          `json:"occurred_ms"`
	Sequence     string          `json:"sequence"`
	Payload      json.RawMessage `json:"payload"`
}

func BuildEventDelivery(eventID, eventType, resourceType, resourceID string, occurredMS int64, payload []byte) ([]byte, string, error) {
	canonical, _, err := ValidateTaskPayload(payload)
	if err != nil || !backendUUID.MatchString(eventID) || occurredMS < 0 ||
		ValidateEventSubscription(EventSubscriptionSpec{
			EventType: eventType, ResourceType: resourceType,
			ResourceID: resourceID, MaxInflight: 1,
		}) != nil {
		return nil, "", ErrEventContract
	}
	delivery := EventDelivery{
		Format: EventDeliveryFormat, EventID: eventID, EventType: eventType,
		ResourceType: resourceType, ResourceID: resourceID,
		OccurredMS: strconv.FormatInt(occurredMS, 10),
		Sequence:   strconv.FormatInt(occurredMS, 10) + "," + eventID,
		Payload:    canonical,
	}
	raw, err := json.Marshal(delivery)
	if err != nil || len(raw) > MaxTaskPayloadBytes {
		return nil, "", ErrEventContract
	}
	return raw, delivery.Sequence, nil
}

func ParseEventDelivery(raw []byte) (EventDelivery, error) {
	var delivery EventDelivery
	if len(raw) == 0 || len(raw) > MaxTaskPayloadBytes ||
		strictTaskBackendJSON(raw, &delivery) != nil ||
		delivery.Format != EventDeliveryFormat ||
		!backendUUID.MatchString(delivery.EventID) ||
		delivery.OccurredMS == "" ||
		ValidateEventSubscription(EventSubscriptionSpec{
			EventType: delivery.EventType, ResourceType: delivery.ResourceType,
			ResourceID: delivery.ResourceID, MaxInflight: 1,
		}) != nil {
		return delivery, ErrEventContract
	}
	occurred, err := strconv.ParseInt(delivery.OccurredMS, 10, 64)
	if err != nil || occurred < 0 ||
		delivery.Sequence != strconv.FormatInt(occurred, 10)+","+delivery.EventID {
		return delivery, ErrEventContract
	}
	canonical, _, err := ValidateTaskPayload(delivery.Payload)
	if err != nil || string(canonical) != string(delivery.Payload) {
		return delivery, ErrEventContract
	}
	return delivery, nil
}

func EventDeliveryOrder(left, right EventDelivery) int {
	leftMS, _ := strconv.ParseInt(left.OccurredMS, 10, 64)
	rightMS, _ := strconv.ParseInt(right.OccurredMS, 10, 64)
	if leftMS < rightMS {
		return -1
	}
	if leftMS > rightMS {
		return 1
	}
	return strings.Compare(left.EventID, right.EventID)
}

func EventOccurrence(eventID string, subscriptionRevision uint64) (string, error) {
	if !backendUUID.MatchString(eventID) || subscriptionRevision < 1 {
		return "", ErrEventContract
	}
	value := "event:" + eventID + ":" +
		strconv.FormatUint(subscriptionRevision, 10)
	if len(value) > 128 {
		return "", ErrEventContract
	}
	return value, nil
}
