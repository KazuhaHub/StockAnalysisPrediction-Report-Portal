package app

import (
	"net/http"
	"strings"

	"github.com/mileusna/useragent"
)

const maxClientHeaderLength = 2048

var capturedClientHintHeaders = []string{
	"Sec-CH-UA",
	"Sec-CH-UA-Mobile",
	"Sec-CH-UA-Platform",
	"Sec-CH-UA-Platform-Version",
	"Sec-CH-UA-Model",
	"Sec-CH-UA-Arch",
	"Sec-CH-UA-Bitness",
	"Sec-CH-UA-Full-Version-List",
}

// ClientSummary is the non-sensitive browser and device description returned to an account holder.
// Raw request headers remain in the administrator-only audit projection.
type ClientSummary struct {
	Browser        string `json:"browser,omitempty"`
	BrowserVersion string `json:"browser_version,omitempty"`
	OS             string `json:"os,omitempty"`
	OSVersion      string `json:"os_version,omitempty"`
	Device         string `json:"device,omitempty"`
	DeviceType     string `json:"device_type,omitempty"`
}

// ClientInfo is the complete client evidence captured with an authentication audit event.
type ClientInfo struct {
	ClientSummary
	UserAgent   string            `json:"user_agent,omitempty"`
	ClientHints map[string]string `json:"client_hints,omitempty"`
}

func (c ClientInfo) Summary() *ClientSummary {
	if c.Browser == "" && c.OS == "" && c.Device == "" && c.DeviceType == "" {
		return nil
	}
	summary := c.ClientSummary
	return &summary
}

func boundedHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxClientHeaderLength {
		return value
	}
	return value[:maxClientHeaderLength]
}

func clientInfoFromRequest(r *http.Request) *ClientInfo {
	if r == nil {
		return nil
	}
	raw := boundedHeader(r.UserAgent())
	parsed := useragent.Parse(raw)
	info := ClientInfo{
		ClientSummary: ClientSummary{
			Browser: parsed.Name, BrowserVersion: parsed.Version,
			OS: parsed.OS, OSVersion: parsed.OSVersion, Device: parsed.Device,
		},
		UserAgent: raw,
	}
	switch {
	case parsed.Bot:
		info.DeviceType = "bot"
	case parsed.Tablet:
		info.DeviceType = "tablet"
	case parsed.Mobile:
		info.DeviceType = "mobile"
	case parsed.Desktop:
		info.DeviceType = "desktop"
	}

	for _, name := range capturedClientHintHeaders {
		if value := boundedHeader(r.Header.Get(name)); value != "" {
			if info.ClientHints == nil {
				info.ClientHints = make(map[string]string)
			}
			info.ClientHints[name] = value
		}
	}
	if platform := strings.Trim(r.Header.Get("Sec-CH-UA-Platform"), ` "`); platform != "" {
		info.OS = platform
	}
	switch r.Header.Get("Sec-CH-UA-Mobile") {
	case "?1":
		info.DeviceType = "mobile"
	case "?0":
		if info.DeviceType == "" {
			info.DeviceType = "desktop"
		}
	}
	if info.UserAgent == "" && len(info.ClientHints) == 0 {
		return nil
	}
	return &info
}

func authDetailWithClient(r *http.Request, detail map[string]any) map[string]any {
	client := clientInfoFromRequest(r)
	if client == nil {
		return detail
	}
	snapshot := make(map[string]any, len(detail)+1)
	for key, value := range detail {
		snapshot[key] = value
	}
	snapshot["client"] = client
	return snapshot
}
