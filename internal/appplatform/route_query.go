package appplatform

import (
	"bytes"
	"encoding/json"
)

// Preserve numeric-field presence and reject null aliases in signed rules.
func (q *RouteQuery) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ErrInvalidDescriptor
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrInvalidDescriptor
		}
	}
	type plain RouteQuery
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*q = RouteQuery(value)
	return nil
}
func validRouteQueryRules(rules []RouteQuery) bool {
	if len(rules) > maxRouteQueryFields {
		return false
	}
	names := map[string]bool{}
	for _, q := range rules {
		if !queryNamePattern.MatchString(q.Name) || q.Name == "constructor" || q.Name == "prototype" || names[q.Name] {
			return false
		}
		names[q.Name] = true
		switch q.Type {
		case "text":
			if q.MaxBytes == nil || *q.MaxBytes < 1 || *q.MaxBytes > maxRouteQueryTextBytes || q.Min != nil || q.Max != nil || q.Values != nil {
				return false
			}
		case "integer":
			if q.Min == nil || q.Max == nil || *q.Min < -maxRouteQueryInteger || *q.Max > maxRouteQueryInteger || *q.Min > *q.Max || q.MaxBytes != nil || q.Values != nil {
				return false
			}
		case "boolean":
			if q.Min != nil || q.Max != nil || q.MaxBytes != nil || q.Values != nil {
				return false
			}
		case "enum":
			if q.Min != nil || q.Max != nil || q.MaxBytes != nil || len(q.Values) < 1 || len(q.Values) > maxRouteQueryValues {
				return false
			}
			seen := map[string]bool{}
			for _, v := range q.Values {
				if !queryEnumPattern.MatchString(v) || seen[v] {
					return false
				}
				seen[v] = true
			}
		default:
			return false
		}
	}
	return true
}
