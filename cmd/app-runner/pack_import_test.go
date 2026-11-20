package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/packimport"
)

func TestPackImportRefusesNonRootAndMissingFlags(t *testing.T) {
	called := false
	pack := func(context.Context, packimport.Options) (packimport.Result, error) {
		called = true
		return packimport.Result{SHA256: strings.Repeat("a", 64), Bytes: 2048, Entries: 3}, nil
	}
	var out bytes.Buffer
	if code := packImport(context.Background(), []string{"--from", "/x", "--out", "/y/.portal-imports/a.tar"}, &out, 1000, pack); code != 1 || called {
		t.Fatalf("non-root accepted: %d %s", code, out.String())
	}
	out.Reset()
	if code := packImport(context.Background(), []string{"--from", "/x"}, &out, 0, pack); code != 2 || called {
		t.Fatalf("missing --out accepted: %d", code)
	}
	out.Reset()
	if code := packImport(context.Background(), []string{"--from", "/x", "--out", "/y/.portal-imports/a.tar"}, &out, 0, pack); code != 0 || !called {
		t.Fatalf("pack not run: %d %s", code, out.String())
	}
	for _, want := range []string{"sha256:  " + strings.Repeat("a", 64), "bytes:   2048", "entries: 3"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q: %s", want, out.String())
		}
	}
}
