package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const probeTimeout = 10 * time.Second

// newTransport returns the HTTP transport shared by both backends. The
// timeout is split into per-stage budgets so big book bodies can take as
// long as they need while hung connections still fail fast. A single
// Client.Timeout is too blunt: a 500 MB CBR over slow Wi-Fi needs more
// than 30 s just to stream the body.
//
// ResponseHeaderTimeout is generous (5 min) because CWA runs calibredb
// export with metadata embedding before it sends the first byte, which
// can take a minute or more for large files. Once the headers arrive,
// the body read has no deadline of its own; the request context bounds
// it (see downloadBodyTimeout in sync.go).
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
}

// newHTTPClient pairs newTransport with the https-to-http downgrade
// guard. Every client that carries credentials or fetches code to run
// (OPDS, updater) is built here so the redirect policy is applied in
// one place.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport:     newTransport(),
		CheckRedirect: rejectSchemeDowngrade,
	}
}

// probeClient runs the wizard's OPDS connection test. The probe sends the
// server password as Basic auth and Go replays the Authorization header
// across a same-host redirect, so it needs the same https-to-http guard as
// the sync client; http.DefaultClient has no redirect policy at all.
var probeClient = newHTTPClient()

// ProbeCWA verifies that host points to a CWA (or compatible) OPDS server
// that accepts the given credentials. Returns nil on success; on failure the
// error message is short and suitable for display on the device.
func ProbeCWA(ctx context.Context, host, user, pass string) error {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("Server URL is not valid. Expected http:// or https://")
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	opdsURL := strings.TrimRight(host, "/") + "/opds"
	req, err := http.NewRequestWithContext(ctx, "GET", opdsURL, nil)
	if err != nil {
		return fmt.Errorf("internal request error: %w", err)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}

	resp, err := probeClient.Do(req)
	if err != nil {
		return classifyTransportError(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == 200:
		// body check below
	case resp.StatusCode == 401:
		return errors.New("Username or password rejected by the server.")
	case resp.StatusCode == 403:
		return errors.New("Credentials accepted but this user has no access.")
	case resp.StatusCode == 404:
		return errors.New("Server is reachable but /opds was not found. Is this a Calibre-Web URL?")
	case resp.StatusCode >= 500:
		return fmt.Errorf("Server error (HTTP %d). Try again later.", resp.StatusCode)
	default:
		return fmt.Errorf("Unexpected response from server (HTTP %d).", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return errors.New("Could not read server response.")
	}
	if !looksLikeOPDS(body) {
		return errors.New("Server responded but not with an OPDS feed. Is this a Calibre-Web URL?")
	}
	return nil
}

// looksLikeOPDS returns true if body starts with an Atom <feed> element in
// the OPDS namespace.
func looksLikeOPDS(body []byte) bool {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		return se.Name.Local == "feed" && se.Name.Space == "http://www.w3.org/2005/Atom"
	}
}

// classifyTransportError maps a net/http transport error to a concise,
// user-readable message covering DNS, timeout, TLS, connection refused,
// a refused https-to-http redirect, and generic network unreachable.
func classifyTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return errors.New("Server did not respond in time.")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errors.New("Server name could not be resolved. Check the URL.")
	}
	if errors.Is(err, errSchemeDowngrade) {
		return errors.New("Server redirected to an insecure http address. Refusing to send the password.")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "x509:"), strings.Contains(msg, "tls:"):
		return errors.New("TLS handshake failed. Is the server's certificate valid?")
	case strings.Contains(msg, "connection refused"):
		return errors.New("Server refused the connection. Is the service running?")
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return errors.New("Server is not reachable on this network.")
	}
	return errors.New("Could not reach the server. Check the URL and network.")
}
