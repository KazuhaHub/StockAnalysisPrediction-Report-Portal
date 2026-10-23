package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type quoteProbeComparisonResult struct {
	Target     quoteProbeTarget       `json:"target"`
	Candidate  string                 `json:"candidate"`
	Status     string                 `json:"status"`
	Error      string                 `json:"error,omitempty"`
	Comparison *quoteShadowComparison `json:"comparison,omitempty"`
}

type quoteProbeComparisonReport struct {
	Version        int                          `json:"version"`
	ManifestSHA256 string                       `json:"manifest_sha256"`
	Primary        string                       `json:"primary"`
	Anchors        [2]string                    `json:"anchors"`
	Results        []quoteProbeComparisonResult `json:"results"`
}

// Replay consumes only local, integrity-checked evidence. Neither a URL from the manifest nor
// a vendor fetcher is executed. Hashes detect accidental corruption, not a malicious capture author.
func compareQuoteProbe(directory, primary string, anchors [2]string) (quoteProbeComparisonReport, error) {
	report := quoteProbeComparisonReport{Version: 1, Primary: primary, Anchors: anchors, Results: []quoteProbeComparisonResult{}}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return report, err
	}
	defer root.Close()
	reader := quoteProbeEvidenceReader{root: root, remaining: 512 << 20}
	body, err := reader.read("manifest.json", 8<<20)
	if err != nil {
		return report, err
	}
	report.ManifestSHA256 = quoteProbeSHA256(body)
	var manifest quoteProbeManifest
	if err := decodeQuoteProbeJSON(body, &manifest); err != nil {
		return report, err
	}
	if err := validateQuoteProbeManifest(manifest, primary, anchors); err != nil {
		return report, err
	}
	type evidence struct {
		bars        []QuoteBar
		unavailable string
	}
	for ti, target := range manifest.Targets {
		entries := make(map[string]evidence, len(manifest.Sources))
		for si, source := range manifest.Sources {
			i := ti*len(manifest.Sources) + si
			record := manifest.Records[i]
			if record.Source != source || record.Target != target || (record.FetchMode != "count" && record.FetchMode != "bounded") {
				return report, fmt.Errorf("record %d does not match declared source/target order or fetch mode", i+1)
			}
			prefix := fmt.Sprintf("%04d", i+1)
			unavailable := record.Error
			if len(record.HTTP) == 0 {
				if unavailable == "" {
					return report, fmt.Errorf("record %d has no HTTP evidence", i+1)
				}
			}
			if len(record.HTTP) > 16 {
				return report, fmt.Errorf("too many HTTP responses in record %d", i+1)
			}
			for j, request := range record.HTTP {
				if request.File == "" {
					if request.Error == "" || request.Status != 0 || request.Bytes != 0 || request.SHA256 != "" {
						return report, fmt.Errorf("invalid missing response in record %d", i+1)
					}
				} else {
					if request.File != fmt.Sprintf("%s-%03d.raw", prefix, j+1) || request.Bytes < 0 || request.Bytes > quoteBodyLimit {
						return report, fmt.Errorf("invalid raw file in record %d", i+1)
					}
					raw, err := reader.read(request.File, quoteBodyLimit)
					if err != nil {
						return report, err
					}
					if int64(len(raw)) != request.Bytes || quoteProbeSHA256(raw) != request.SHA256 {
						return report, fmt.Errorf("raw evidence integrity failure: %s", request.File)
					}
				}
				if request.Error != "" || request.Truncated || request.Status < 200 || request.Status >= 400 {
					unavailable = "HTTP evidence failed or was truncated"
				}
				if j == len(record.HTTP)-1 && (request.Status < 200 || request.Status >= 300) {
					unavailable = "no successful final HTTP response"
				}
			}
			var response QuoteResp
			if record.ParsedFile == "" {
				if record.ParsedSHA256 != "" || unavailable == "" {
					return report, fmt.Errorf("record %d lacks parsed evidence", i+1)
				}
			} else {
				if record.ParsedFile != prefix+".parsed.json" {
					return report, fmt.Errorf("invalid parsed filename in record %d", i+1)
				}
				parsed, err := reader.read(record.ParsedFile, 2<<20)
				if err != nil {
					return report, err
				}
				if quoteProbeSHA256(parsed) != record.ParsedSHA256 {
					return report, fmt.Errorf("parsed evidence integrity failure: %s", record.ParsedFile)
				}
				if err := decodeQuoteProbeJSON(parsed, &response); err != nil {
					return report, err
				}
				if response.Source != source || response.Market != target.Market || response.Symbol != target.Code || response.Adjusted || response.Cached {
					return report, fmt.Errorf("parsed identity, adjustment or cache mismatch in record %d", i+1)
				}
				if response.BarsUnavailable != "" {
					unavailable = "history unavailable: " + response.BarsUnavailable
				}
				if len(response.Bars) > 0 {
					if response.BarsSource != source {
						return report, fmt.Errorf("bar source mismatch in record %d", i+1)
					}
					if _, err := indexQuoteShadowBars(response.Bars); err != nil {
						return report, fmt.Errorf("invalid parsed bars in record %d: %w", i+1, err)
					}
				}
			}
			bars := []QuoteBar{}
			for _, bar := range response.Bars {
				if bar.Date >= manifest.From && bar.Date <= manifest.To {
					bars = append(bars, bar)
				}
			}
			if len(bars) == 0 {
				unavailable = "no bars in requested window"
			}
			entries[source] = evidence{bars: bars, unavailable: unavailable}
		}
		for _, candidate := range manifest.Sources {
			if candidate == primary {
				continue
			}
			result := quoteProbeComparisonResult{Target: target, Candidate: candidate, Status: "unavailable"}
			p, c := entries[primary], entries[candidate]
			switch {
			case p.unavailable != "":
				result.Error = "primary: " + p.unavailable
			case c.unavailable != "":
				result.Error = "candidate: " + c.unavailable
			default:
				comparison, err := compareQuoteShadow(p.bars, c.bars, anchors)
				if err != nil {
					result.Error = err.Error()
				} else {
					result.Comparison = &comparison
					result.Status = "mismatch"
					if comparison.Match {
						result.Status = "match"
					}
				}
			}
			report.Results = append(report.Results, result)
		}
	}
	return report, nil
}

func validateQuoteProbeManifest(m quoteProbeManifest, primary string, anchors [2]string) error {
	w, err := quoteParseWindow(m.From, m.To)
	if err != nil || !w.bounded() || w.from != m.From || w.to != m.To {
		return fmt.Errorf("invalid manifest window")
	}
	a, err := quoteParseWindow(anchors[0], anchors[1])
	if err != nil || !a.bounded() || a.from != anchors[0] || a.to != anchors[1] || a.from < w.from || a.to > w.to {
		return fmt.Errorf("anchors must be canonical dates inside capture window")
	}
	if m.Version != 1 || !m.Complete {
		return fmt.Errorf("unsupported or incomplete capture")
	}
	if _, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil {
		return fmt.Errorf("invalid capture timestamp")
	}
	if len(m.Sources) < 2 || len(m.Sources) > 16 || len(m.Targets) == 0 || len(m.Targets) > 100 || len(m.Records) != len(m.Sources)*len(m.Targets) {
		return fmt.Errorf("incomplete source/target matrix")
	}
	sources := map[string]bool{}
	for _, source := range m.Sources {
		if source == "" || len(source) > 64 || sources[source] {
			return fmt.Errorf("invalid or duplicate declared source")
		}
		sources[source] = true
	}
	if !sources[primary] {
		return fmt.Errorf("primary is not a captured source")
	}
	targets := map[quoteProbeTarget]bool{}
	for _, target := range m.Targets {
		parsed, err := quoteTargetFor(target.Market, target.Code)
		if err != nil || parsed.Code != target.Code || targets[target] {
			return fmt.Errorf("invalid or duplicate declared target")
		}
		targets[target] = true
	}
	return nil
}

type quoteProbeEvidenceReader struct {
	root      *os.Root
	remaining int64
}

func (reader *quoteProbeEvidenceReader) read(name string, limit int64) ([]byte, error) {
	info, err := reader.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || info.Size() > reader.remaining {
		return nil, fmt.Errorf("evidence is not a bounded regular file: %s", name)
	}
	file, err := reader.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("evidence file changed: %s", name)
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit || int64(len(body)) > reader.remaining {
		return nil, fmt.Errorf("evidence limit exceeded: %s", name)
	}
	reader.remaining -= int64(len(body))
	return body, nil
}

func decodeQuoteProbeJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing evidence JSON")
	}
	return nil
}
