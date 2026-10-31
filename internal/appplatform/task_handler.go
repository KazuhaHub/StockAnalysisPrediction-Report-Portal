package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

var ErrTaskHandler = errors.New("application task handler unavailable")

const (
	MaxTaskPayloadBytes = 8192
	TaskHandlerBackend  = "app.backend.task"
	TaskHandlerDigest   = "scheduled-digest.run"
)

type TaskHandlerSpec struct {
	ID                   string
	AllowedAppID         string
	RequiredCapabilities []string
}

var taskHandlers = []TaskHandlerSpec{
	{ID: TaskHandlerBackend, RequiredCapabilities: []string{"background.run", "tasks.schedule"}},
	{ID: TaskHandlerDigest, AllowedAppID: "scheduled-digest", RequiredCapabilities: []string{"background.run", "reports.read", "tasks.schedule"}},
}

func ResolveTaskHandler(appID, handler string, granted map[string]bool) (TaskHandlerSpec, error) {
	if !slugPattern.MatchString(appID) || len(handler) < 1 || len(handler) > 64 {
		return TaskHandlerSpec{}, ErrTaskHandler
	}
	for _, spec := range taskHandlers {
		if spec.ID != handler || spec.AllowedAppID != "" && spec.AllowedAppID != appID {
			continue
		}
		for _, capability := range spec.RequiredCapabilities {
			if !granted[capability] {
				return TaskHandlerSpec{}, ErrTaskHandler
			}
		}
		return spec, nil
	}
	return TaskHandlerSpec{}, ErrTaskHandler
}

var deniedTaskPayloadKeys = map[string]bool{
	"url": true, "uri": true, "endpoint": true, "callback": true,
	"command": true, "cmd": true, "executable": true, "path": true,
	"recipient": true, "recipients": true, "destination": true,
	"webhook": true, "token": true, "secret": true, "password": true,
	"method": true, "headers": true, "table": true, "sql": true,
}

func taskPayloadValue(value any, depth int) error {
	if depth > 8 {
		return ErrTaskHandler
	}
	switch typed := value.(type) {
	case nil, bool, string, json.Number:
		if text, ok := typed.(string); ok && len(text) > 4096 {
			return ErrTaskHandler
		}
		return nil
	case []any:
		if len(typed) > 128 {
			return ErrTaskHandler
		}
		for _, item := range typed {
			if taskPayloadValue(item, depth+1) != nil {
				return ErrTaskHandler
			}
		}
		return nil
	case map[string]any:
		if len(typed) > 64 {
			return ErrTaskHandler
		}
		for key, item := range typed {
			if deniedTaskPayloadKeys[strings.ToLower(key)] || len(key) > 64 || taskPayloadValue(item, depth+1) != nil {
				return ErrTaskHandler
			}
		}
		return nil
	default:
		return ErrTaskHandler
	}
}

func taskJSONUnique(decoder *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !taskJSONUnique(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !taskJSONUnique(decoder, depth+1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func ValidateTaskPayload(raw []byte) ([]byte, string, error) {
	if len(raw) == 0 || len(raw) > MaxTaskPayloadBytes || !utf8JSON(raw) {
		return nil, "", ErrTaskHandler
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	if !taskJSONUnique(scan, 0) {
		return nil, "", ErrTaskHandler
	}
	if _, err := scan.Token(); err != io.EOF {
		return nil, "", ErrTaskHandler
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil || taskPayloadValue(value, 0) != nil {
		return nil, "", ErrTaskHandler
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, "", ErrTaskHandler
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > MaxTaskPayloadBytes {
		return nil, "", ErrTaskHandler
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func utf8JSON(raw []byte) bool {
	return json.Valid(raw) && bytes.Equal(bytes.ToValidUTF8(raw, nil), raw)
}

func SortedTaskResources(resources []string) []string {
	out := append([]string(nil), resources...)
	sort.Strings(out)
	return out
}
