package transport

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
)

// proxyCloseReasons are the reasons a tunnel's close may be counted under in a
// rollup.
var proxyCloseReasons = map[string]bool{
	broker.ProxyCloseEOF: true, broker.ProxyCloseIdle: true, broker.ProxyCloseLifetime: true, broker.ProxyCloseBudget: true,
	broker.ProxyCloseSession: true, broker.ProxyClosePolicy: true, broker.ProxyCloseSNIMismatch: true, broker.ProxyCloseError: true,
}

// proxyRefusalCodes are the codes a refusal row may carry: the refusals of doc 184
// section 8 and the sni_mismatch that is reported after the tunnel opened.
var proxyRefusalCodes = map[string]bool{
	string(proxyforwarder.RefusalNotGranted): true, string(proxyforwarder.RefusalHostNotReviewed): true,
	string(proxyforwarder.RefusalPortNotPermitted): true, string(proxyforwarder.RefusalAddressDenied): true,
	string(proxyforwarder.RefusalResolveFailed): true, string(proxyforwarder.RefusalConnectFailed): true,
	string(proxyforwarder.RefusalBusy): true, string(proxyforwarder.RefusalBudgetSpent): true,
	string(proxyforwarder.RefusalUnavailable): true, broker.ProxyCloseSNIMismatch: true,
}

// EncodeEgressProxyReport encodes one report as closed JSON.
func EncodeEgressProxyReport(r broker.ProxyReport) ([]byte, error) {
	if !validEgressProxyReport(r) {
		return nil, ErrEgressProtocol
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > egressProxyReportMaxBytes {
		return nil, ErrEgressProtocol
	}
	return raw, nil
}

// DecodeEgressProxyReport accepts only the exact canonical encoding of a valid
// report.
func DecodeEgressProxyReport(payload []byte) (broker.ProxyReport, error) {
	var r broker.ProxyReport
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if len(payload) == 0 || len(payload) > egressProxyReportMaxBytes || d.Decode(&r) != nil {
		return broker.ProxyReport{}, ErrEgressProtocol
	}
	canonical, err := EncodeEgressProxyReport(r)
	if err != nil || !bytes.Equal(canonical, payload) {
		return broker.ProxyReport{}, ErrEgressProtocol
	}
	return r, nil
}

func validProxyReportHost(host string) bool {
	return host == broker.ProxyOtherHost || host == broker.ProxyUnknownHost || (len(host) <= 253 && host == strings.ToLower(host) && validReportName(host))
}

func validReportName(host string) bool {
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func validEgressProxyReport(r broker.ProxyReport) bool {
	switch r.Kind {
	case broker.ProxyReportRollup:
		hour, err := time.Parse(time.RFC3339, r.Hour)
		if err != nil || hour.UTC().Format(time.RFC3339) != r.Hour || hour.Unix()%3600 != 0 ||
			len(r.Hosts) < 1 || len(r.Hosts) > broker.ProxyMaxRollupHosts ||
			r.Host != "" || r.Code != "" || r.Count != 0 || r.Day != "" || r.Bytes != 0 || r.Budget != 0 {
			return false
		}
		previous := ""
		for _, h := range r.Hosts {
			if !validProxyReportHost(h.Host) || h.Host <= previous || h.Tunnels < 0 || h.BytesIn < 0 || h.BytesOut < 0 || h.Closes == nil || len(h.Closes) > len(proxyCloseReasons) {
				return false
			}
			previous = h.Host
			for reason, n := range h.Closes {
				if !proxyCloseReasons[reason] || n < 0 {
					return false
				}
			}
		}
		return true
	case broker.ProxyReportRefusal:
		return r.Hour == "" && r.Hosts == nil && validProxyReportHost(r.Host) && r.Host != broker.ProxyOtherHost &&
			proxyRefusalCodes[r.Code] && r.Count >= 1 && r.Day == "" && r.Bytes == 0 && r.Budget == 0
	case broker.ProxyReportUsage:
		day, err := time.Parse("2006-01-02", r.Day)
		return err == nil && day.Format("2006-01-02") == r.Day && r.Hour == "" && r.Hosts == nil && r.Host == "" && r.Code == "" && r.Count == 0 &&
			r.Bytes >= 1 && r.Budget >= appplatform.ProxyDailyBytesMin && r.Budget <= appplatform.ProxyDailyBytesMax
	}
	return false
}
