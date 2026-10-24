package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func scriptedSMTP(t *testing.T, serve func(net.Conn)) Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SMTP fixture did not stop")
		}
	})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		serve(conn)
	}()
	return Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "sender@example.com", Security: "none"}
}

func TestAttemptCancellationClosesBlockedGreeting(t *testing.T) {
	connected := make(chan struct{})
	cfg := scriptedSMTP(t, func(conn net.Conn) {
		close(connected)
		var b [1]byte
		conn.Read(b[:])
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cfg.Attempt(ctx, []string{"recipient@example.com"}, "Test", "body"); done <- err }()
	<-connected
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled attempt succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt SMTP greeting")
	}
}

func TestAttemptTracksDataAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, reply         string
		accepted, uncertain bool
	}{
		{"accepted with failed quit", "250 accepted\r\n", true, false},
		{"explicit data rejection", "550 rejected\r\n", false, false},
		{"lost data response", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := scriptedSMTP(t, func(conn net.Conn) {
				fmt.Fprint(conn, "220 test ESMTP\r\n")
				r := bufio.NewReader(conn)
				data := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if data {
						if line == ".\r\n" {
							fmt.Fprint(conn, tc.reply)
							return
						}
						continue
					}
					if strings.HasPrefix(line, "DATA") {
						data = true
						fmt.Fprint(conn, "354 send\r\n")
						continue
					}
					fmt.Fprint(conn, "250 OK\r\n")
				}
			})
			result, err := cfg.Attempt(context.Background(), []string{"recipient@example.com"}, "Test", "body")
			if result.Accepted != tc.accepted || result.Uncertain != tc.uncertain {
				t.Fatalf("result=%+v, want accepted=%v uncertain=%v", result, tc.accepted, tc.uncertain)
			}
			if (err == nil) != tc.accepted {
				t.Fatalf("error=%v, accepted=%v", err, tc.accepted)
			}
		})
	}
}

func TestAttemptDeadlineCoversProtocolStages(t *testing.T) {
	for _, stage := range []string{"greeting", "ehlo", "data", "tls"} {
		t.Run(stage, func(t *testing.T) {
			cfg := scriptedSMTP(t, func(conn net.Conn) {
				if stage == "greeting" || stage == "tls" {
					var b [4096]byte
					for {
						if _, err := conn.Read(b[:]); err != nil {
							return
						}
					}
				}
				fmt.Fprint(conn, "220 test ESMTP\r\n")
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if stage == "ehlo" || strings.HasPrefix(line, "DATA") {
						for {
							if _, err := r.ReadString('\n'); err != nil {
								return
							}
						}
					}
					fmt.Fprint(conn, "250 OK\r\n")
				}
			})
			if stage == "tls" {
				cfg.Security = "tls"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result, err := cfg.Attempt(ctx, []string{"recipient@example.com"}, "Test", "body")
			if err == nil || result.Accepted || result.Uncertain {
				t.Fatalf("stalled %s result=%+v error=%v", stage, result, err)
			}
		})
	}
}
