package app

import (
	"context"
	"strings"
	"testing"
)

// The reset and verification links are built from the admin's public_url setting. It is trusted
// input, but it is still written into an HTML attribute, so a quote in it must stay inside the
// href rather than end the attribute and inject markup into a message sent to a user.
func TestMailLinksStayInsideTheHrefAttribute(t *testing.T) {
	const hostile = `https://portal.example/"><b>x</b>`

	capture := func(s *Server) *string {
		var got string
		s.mailFn = func(_ []string, _ string, body string) error { got = body; return nil }
		return &got
	}
	assertEscaped := func(t *testing.T, body string) {
		t.Helper()
		if body == "" {
			t.Fatal("no mail body was captured")
		}
		if strings.Contains(body, `"><b>`) {
			t.Fatalf("the link broke out of the href attribute:\n%s", body)
		}
		if !strings.Contains(body, `href="https://portal.example/&#34;&gt;&lt;b&gt;x&lt;/b&gt;`) {
			t.Fatalf("the href does not carry the escaped link:\n%s", body)
		}
	}

	t.Run("password reset", func(t *testing.T) {
		s := regServer(t)
		got := capture(s)
		u := &User{Username: "alice", Email: "alice@example.com"}
		if err := s.sendResetEmailContext(context.Background(), u, hostile+"/reset?token=t"); err != nil {
			t.Fatal(err)
		}
		assertEscaped(t, *got)
	})

	t.Run("email verification", func(t *testing.T) {
		s := regServer(t)
		got := capture(s)
		s.st.SetSetting("public_url", hostile)
		if err := s.sendVerifyEmailContext(context.Background(), "alice@example.com", "t"); err != nil {
			t.Fatal(err)
		}
		assertEscaped(t, *got)
	})
}
