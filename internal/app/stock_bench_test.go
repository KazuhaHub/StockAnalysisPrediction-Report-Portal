package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// BenchmarkStockPage measures GET /api/stock/{symbol} on a file-backed SQLite store, whose pool is a
// single connection, while writer goroutines keep the connection busy. It runs the handler as it was
// (reference) and as it is, each with and without the statement cache, and reports p50 and p95
// latency of the readers. The "single-connection" case switches the read pool off, which is the shape
// before it; the others run with the pool a file-backed store now carries.
//
//	go test ./internal/app -run xxx -bench BenchmarkStockPage -benchtime 3000x
func BenchmarkStockPage(b *testing.B) {
	cases := []struct {
		name    string
		grouped bool
		cache   bool
		single  bool // reads on the one writer connection, as before the read pool
	}{
		{"reference/no-cache", false, false, false},
		{"reference/cache", false, true, false},
		{"grouped/no-cache", true, false, false},
		{"grouped/cache", true, true, false},
		{"grouped/cache/single-connection", true, true, true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			st, err := OpenStore("sqlite", filepath.Join(b.TempDir(), "portal.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			if c.single {
				st.closeReadPool()
			}
			if !c.cache {
				st.stmts.close()
				st.stmts = nil
				if st.rstmts != nil {
					st.rstmts.close()
					st.rstmts = nil
				}
			}
			s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
			s.names = LoadNames(b.TempDir(), st)
			st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
			for d := 0; d < 30; d++ {
				for k := 0; k < 4; k++ {
					st.UpsertReport(Rep{Symbol: "600519", Date: fmt.Sprintf("2026-06-%02d", d+1), RType: fmt.Sprintf("类型%d", k),
						Title: fmt.Sprintf("600519 类型%d", k), MD: "# 正文\n\n内容"})
				}
			}
			handler := s.apiStockReference
			if c.grouped {
				handler = s.apiStock
			}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			var writes atomic.Int64
			for w := 0; w < 2; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
						}
						st.SetSetting(fmt.Sprintf("bench_%d", w), fmt.Sprint(i))
						time.Sleep(time.Millisecond) // paced: a writer that never pauses measures fsync, not the reader
						writes.Add(1)
					}
				}(w)
			}
			var mu sync.Mutex
			var lat []time.Duration
			b.ResetTimer()
			b.SetParallelism(1) // x GOMAXPROCS readers
			b.RunParallel(func(pb *testing.PB) {
				var mine []time.Duration
				for pb.Next() {
					req := httptest.NewRequest(http.MethodGet, "/api/stock/600519?date=2026-06-03", nil)
					req.SetPathValue("symbol", "600519")
					rec := httptest.NewRecorder()
					t0 := time.Now()
					handler(rec, req, "alice")
					mine = append(mine, time.Since(t0))
					if rec.Code != 200 {
						b.Errorf("status %d", rec.Code)
						return
					}
				}
				mu.Lock()
				lat = append(lat, mine...)
				mu.Unlock()
			})
			b.StopTimer()
			close(stop)
			wg.Wait()
			sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
			if len(lat) > 0 {
				b.ReportMetric(float64(lat[len(lat)/2].Microseconds()), "p50-us")
				b.ReportMetric(float64(lat[len(lat)*95/100].Microseconds()), "p95-us")
			}
			b.ReportMetric(float64(writes.Load())/b.Elapsed().Seconds(), "writes/s")
		})
	}
}
