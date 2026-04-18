package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLooksLikeOPDS(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "CWA OPDS feed (minimal)",
			body: `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>CWA</title></feed>`,
			want: true,
		},
		{
			name: "CWA OPDS feed (with extra xmlns)",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms/">
  <title>CWA</title>
</feed>`,
			want: true,
		},
		{
			name: "non-Atom <feed>",
			body: `<?xml version="1.0"?><feed>nope</feed>`,
			want: false,
		},
		{
			name: "RSS 2.0",
			body: `<?xml version="1.0"?><rss version="2.0"><channel/></rss>`,
			want: false,
		},
		{
			name: "HTML login page",
			body: `<!DOCTYPE html><html><body>Log in</body></html>`,
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeOPDS([]byte(tc.body)); got != tc.want {
				t.Errorf("looksLikeOPDS = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeCWA_BadURL(t *testing.T) {
	cases := []string{"", "not a url", "ftp://x", "example.com"}
	for _, host := range cases {
		t.Run(host, func(t *testing.T) {
			err := ProbeCWA(context.Background(), host, "", "")
			if err == nil || !strings.Contains(err.Error(), "not valid") {
				t.Errorf("ProbeCWA(%q) = %v, want 'not valid' error", host, err)
			}
		})
	}
}

func TestProbeCWA_StatusCodes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{"401 unauthorized", 401, "rejected"},
		{"403 forbidden", 403, "no access"},
		{"404 not found", 404, "not found"},
		{"500 server error", 500, "Server error"},
		{"503 unavailable", 503, "Server error"},
		{"418 teapot", 418, "Unexpected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := ProbeCWA(context.Background(), srv.URL, "u", "p")
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("ProbeCWA status=%d = %v, want substring %q", tc.status, err, tc.wantSubstr)
			}
		})
	}
}

func TestProbeCWA_Non_OPDS_Body(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html>login</html>`))
	}))
	defer srv.Close()
	err := ProbeCWA(context.Background(), srv.URL, "u", "p")
	if err == nil || !strings.Contains(err.Error(), "not with an OPDS") {
		t.Errorf("got %v, want 'not with an OPDS' error", err)
	}
}

func TestProbeCWA_Success(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"/>`))
	}))
	defer srv.Close()

	if err := ProbeCWA(context.Background(), srv.URL, "alice", "hunter2"); err != nil {
		t.Errorf("ProbeCWA = %v, want nil", err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Basic Auth header = %q, want 'Basic ' prefix", gotAuth)
	}
}
