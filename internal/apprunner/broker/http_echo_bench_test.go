package broker

import (
	"bytes"
	"fmt"
	"testing"
)

// BenchmarkStreamEchoGuard pushes 1 MiB of ordinary event data through the guard
// in chunks of 1 byte to 4 KiB, with a 47-byte Bearer value.
func BenchmarkStreamEchoGuard(b *testing.B) {
	value := []byte("Bearer sk-0123456789abcdefghijklmnopqrstuvwxyz")
	stream := bytes.Repeat([]byte(`{"event":"tick","n":1}`+"\n"), (1<<20)/23+1)[:1<<20]
	for _, chunk := range []int{1, 16, 256, 4096} {
		b.Run(fmt.Sprintf("chunk%d", chunk), func(b *testing.B) {
			b.SetBytes(int64(len(stream)))
			for i := 0; i < b.N; i++ {
				g := newHTTPStreamEchoGuard("Authorization", value)
				for off := 0; off < len(stream); off += chunk {
					if _, hit := g.Accept(stream[off:min(off+chunk, len(stream))]); hit {
						b.Fatal("false positive")
					}
				}
				g.Flush()
			}
		})
	}
}
