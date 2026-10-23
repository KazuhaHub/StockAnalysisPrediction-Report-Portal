package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type quoteProbeTarget struct {
	Market string `json:"market"`
	Code   string `json:"code"`
}

type quoteProbeCaptureOptions struct {
	Directory string
	From, To  string
	Sources   []string
	Targets   []quoteProbeTarget
}

type quoteProbeHTTP struct {
	URL         string `json:"url"`
	CapturedAt  string `json:"captured_at"`
	ContentType string `json:"content_type,omitempty"`
	Status      int    `json:"status"`
	File        string `json:"file,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
	Error       string `json:"error,omitempty"`
}

type quoteProbeRecord struct {
	Source       string           `json:"source"`
	Target       quoteProbeTarget `json:"target"`
	FetchMode    string           `json:"fetch_mode"`
	HTTP         []quoteProbeHTTP `json:"http"`
	ParsedFile   string           `json:"parsed_file,omitempty"`
	ParsedSHA256 string           `json:"parsed_sha256,omitempty"`
	Error        string           `json:"error,omitempty"`
}

type quoteProbeManifest struct {
	Version   int                `json:"version"`
	CreatedAt string             `json:"created_at"`
	From      string             `json:"from"`
	To        string             `json:"to"`
	Complete  bool               `json:"complete"`
	Sources   []string           `json:"sources"`
	Targets   []quoteProbeTarget `json:"targets"`
	Records   []quoteProbeRecord `json:"records"`
}

// The override is private and context-local. A probe never swaps the process-wide vendor client
// or captures unrelated server traffic; ordinary requests keep using the existing pooled client.
type quoteProbeClientKey struct{}

func vendorHTTPClient(ctx context.Context) *http.Client {
	if client, ok := ctx.Value(quoteProbeClientKey{}).(*http.Client); ok && client != nil {
		return client
	}
	return vendorClient
}

// captureQuoteProbe has no Store/config argument and opens no database. Partial evidence is kept
// on failure; only a newly created private directory is accepted, never an overwrite or cleanup.
func captureQuoteProbe(ctx context.Context, options quoteProbeCaptureOptions, registry []quoteSource, client *http.Client) (manifest quoteProbeManifest, err error) {
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	window, err := quoteParseWindow(options.From, options.To)
	if err != nil || !window.bounded() {
		return manifest, fmt.Errorf("capture requires a bounded valid window")
	}
	if options.Directory == "" || len(options.Sources) == 0 || len(options.Sources) > 16 || len(options.Targets) == 0 || len(options.Targets) > 100 {
		return manifest, fmt.Errorf("capture requires a new output directory, 1-16 sources and 1-100 targets")
	}
	byName := map[string]quoteSource{}
	for _, source := range registry {
		byName[source.name] = source
	}
	seenSources := map[string]bool{}
	for _, name := range options.Sources {
		if _, have := byName[name]; !have || seenSources[name] {
			return manifest, fmt.Errorf("unknown or duplicate source %q", name)
		}
		seenSources[name] = true
	}
	targets := make([]quoteProbeTarget, 0, len(options.Targets))
	seenTargets := map[string]bool{}
	for _, target := range options.Targets {
		parsed, err := quoteTargetFor(target.Market, target.Code)
		if err != nil {
			return manifest, err
		}
		key := parsed.Market.id + ":" + parsed.Code
		if seenTargets[key] {
			return manifest, fmt.Errorf("duplicate target %s", key)
		}
		seenTargets[key] = true
		targets = append(targets, quoteProbeTarget{Market: parsed.Market.id, Code: parsed.Code})
	}
	directory, err := filepath.Abs(options.Directory)
	if err != nil {
		return manifest, err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return manifest, fmt.Errorf("reserve new capture directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, fmt.Errorf("open reserved capture directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	manifest = quoteProbeManifest{Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		From: window.from, To: window.to, Sources: append([]string{}, options.Sources...), Targets: targets, Records: []quoteProbeRecord{}}
	defer func() {
		body, marshalErr := json.MarshalIndent(manifest, "", "  ")
		if marshalErr == nil {
			marshalErr = writeQuoteProbeFile(root, "manifest.json", body)
		}
		err = errors.Join(err, marshalErr)
	}()
	if client == nil {
		client = vendorClient
	}
	for _, target := range targets {
		for _, name := range options.Sources {
			if err := ctx.Err(); err != nil {
				return manifest, err
			}
			recorder := &quoteProbeRecorder{root: root, prefix: fmt.Sprintf("%04d", len(manifest.Records)+1), base: client.Transport}
			if recorder.base == nil {
				recorder.base = http.DefaultTransport
			}
			captureClient := *client
			captureClient.Transport = recorder
			captureCtx := context.WithValue(ctx, quoteProbeClientKey{}, &captureClient)
			source := byName[name]
			record := quoteProbeRecord{Source: name, Target: target, FetchMode: "count"}
			var response *QuoteResp
			var fetchErr error
			if source.fetchRange != nil {
				record.FetchMode = "bounded"
				response, fetchErr = source.fetchRange(captureCtx, target.Market, target.Code, window.from, window.to)
			} else {
				response, fetchErr = source.call(captureCtx, target.Market, target.Code, quoteMaxBars, quoteIntervalDaily, quoteWindow{})
			}
			record.HTTP, err = recorder.snapshot()
			if fetchErr != nil {
				record.Error = fetchErr.Error()
			} else if response != nil && response.BarsUnavailable != "" {
				record.Error = "history unavailable: " + response.BarsUnavailable
			}
			if response != nil && err == nil {
				var body []byte
				body, err = json.MarshalIndent(response, "", "  ")
				if err == nil {
					record.ParsedFile = recorder.prefix + ".parsed.json"
					record.ParsedSHA256 = quoteProbeSHA256(body)
					err = writeQuoteProbeFile(root, record.ParsedFile, body)
				}
			} else if response == nil && fetchErr == nil {
				record.Error = "source returned no parsed response"
			}
			manifest.Records = append(manifest.Records, record)
			if err != nil {
				return manifest, err
			}
			if err := ctx.Err(); err != nil {
				return manifest, err
			}
		}
	}
	rootInfo, rootErr := root.Stat(".")
	pathInfo, pathErr := os.Lstat(directory)
	if rootErr != nil || pathErr != nil {
		return manifest, fmt.Errorf("verify capture directory: %w", errors.Join(rootErr, pathErr))
	}
	if !os.SameFile(rootInfo, pathInfo) {
		return manifest, fmt.Errorf("capture directory was replaced or moved")
	}
	manifest.Complete = true
	return manifest, nil
}

type quoteProbeRecorder struct {
	mu       sync.Mutex
	root     *os.Root
	prefix   string
	base     http.RoundTripper
	requests []quoteProbeHTTP
	localErr error
}

func (recorder *quoteProbeRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	response, requestErr := recorder.base.RoundTrip(request)
	entry := quoteProbeHTTP{URL: request.URL.String(), CapturedAt: time.Now().UTC().Format(time.RFC3339)}
	var body []byte
	if response != nil {
		entry.Status = response.StatusCode
		entry.ContentType = response.Header.Get("Content-Type")
		var readErr error
		if response.Body != nil {
			body, readErr = io.ReadAll(io.LimitReader(response.Body, quoteBodyLimit+1))
			requestErr = errors.Join(requestErr, readErr, response.Body.Close())
		} else if response.ContentLength > 0 {
			requestErr = errors.Join(requestErr, fmt.Errorf("response body is missing"))
		}
		if len(body) > quoteBodyLimit {
			body = body[:quoteBodyLimit]
			entry.Truncated = true
			requestErr = errors.Join(requestErr, fmt.Errorf("capture body exceeds %d bytes", quoteBodyLimit))
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
	} else if requestErr == nil {
		requestErr = fmt.Errorf("transport returned no response")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if response != nil {
		entry.File = fmt.Sprintf("%s-%03d.raw", recorder.prefix, len(recorder.requests)+1)
		entry.Bytes, entry.SHA256 = int64(len(body)), quoteProbeSHA256(body)
		if err := writeQuoteProbeFile(recorder.root, entry.File, body); err != nil {
			recorder.localErr = errors.Join(recorder.localErr, err)
			requestErr = errors.Join(requestErr, err)
		}
	}
	if requestErr != nil {
		entry.Error = requestErr.Error()
	}
	recorder.requests = append(recorder.requests, entry)
	if requestErr != nil {
		return nil, requestErr
	}
	return response, nil
}

func (recorder *quoteProbeRecorder) snapshot() ([]quoteProbeHTTP, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]quoteProbeHTTP{}, recorder.requests...), recorder.localErr
}

func quoteProbeSHA256(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func writeQuoteProbeFile(root *os.Root, name string, body []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
