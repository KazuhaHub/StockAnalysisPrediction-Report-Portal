package appplatform

import (
	"fmt"
	"testing"
)

func BenchmarkFrontendManySmallFiles(b *testing.B) {
	entries := []frontendTestEntry{{name: "index.html", body: "<!doctype html>"}}
	for i := 0; i < 1023; i++ {
		entries = append(entries, frontendTestEntry{name: fmt.Sprintf("assets/a%04d.js", i), body: "void 0;"})
	}
	raw := frontendTestArchive(b, entries...)
	manifest := frontendManifest(b, raw)
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := InspectFrontend(manifest, raw); err != nil {
			b.Fatal(err)
		}
	}
}
