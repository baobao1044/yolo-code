//go:build providercheck

// Live reachability check for the provider registry. Run it deliberately:
//
//	go test -tags=providercheck ./internal/cognitive/ -run Live -v
//
// It is behind a tag for the same reason internal/event/golden_test.go is: it
// asserts something the ordinary suite cannot. This one needs outbound DNS and
// TLS, so it must never run in CI — a firewalled runner would report the whole
// registry dead, which is worse than not checking.
//
// The registry is a table of claims about other people's infrastructure, and
// those claims rot on someone else's schedule. Two presets had already rotted
// through when this was first run (see providers_reachability_test.go for what
// it found and what was done about it). Nothing in the package could see it:
// every existing test checks the shape of the table, and a preset naming a host
// that was withdrawn a year ago is perfectly well-shaped.
//
// What this proves and what it does not, stated plainly because the distinction
// is the whole value of the test:
//
//   - DNS failure is conclusive. No address record means no connection, so the
//     preset cannot work for anyone, and neither the key nor the model name ever
//     comes into it. That is the class of defect worth failing a test over.
//   - 401 and 403 prove the host is alive and terminating TLS. They do NOT prove
//     the path is right: an API gateway usually authenticates before it routes,
//     so a wrong path under a live host answers 401 exactly like a right one.
//     The cohere preset was wrong in precisely this way and looked healthy.
//   - Model names are not checked here at all. Most providers gate GET /models
//     behind a key, so verifying a DefaultModel needs credentials this test does
//     not have and should not want.
//
// So a green run means "every advertised host exists", not "every preset works".
// It is the strongest claim obtainable without credentials, and it is strictly
// more than the zero the registry had.

package cognitive

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLiveProviderHostsResolve is the conclusive half: every advertised host
// must have an address record.
func TestLiveProviderHostsResolve(t *testing.T) {
	for _, p := range ListProviders() {
		p := p
		if skipReason(p) != "" {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()
			host := hostOf(t, p.BaseURL)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			addrs, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				t.Fatalf("%s does not resolve: %v\n\n"+
					"This preset cannot open a connection, so it fails for every user "+
					"on every run. Either repoint it at the provider's current host or "+
					"remove it, and add the host to hostsConfirmedDead in "+
					"providers_reachability_test.go so the offline suite catches a revert.",
					host, err)
			}
			if len(addrs) == 0 {
				t.Fatalf("%s resolved to no addresses", host)
			}
		})
	}
}

// TestLiveProviderEndpointsAnswer is the weaker, still-useful half. It sends an
// unauthenticated request — no key, no credentials of any kind — and only
// checks that something on the other end replies like an API rather than like a
// parked domain or a 404.
//
// A 404 on the POST is not automatically wrong: some gateways answer 404 to an
// unauthenticated POST and 401 to a GET on the same prefix, which is why a 404
// falls back to GET /models before it is reported. Anything still 404 after
// that is flagged, not failed, because "this path is wrong" needs a human to
// confirm against the provider's docs.
func TestLiveProviderEndpointsAnswer(t *testing.T) {
	client := &http.Client{Timeout: 20 * time.Second}
	for _, p := range ListProviders() {
		p := p
		if skipReason(p) != "" {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()
			code, err := probe(client, http.MethodPost, p.BaseURL+"/chat/completions")
			if err != nil {
				t.Fatalf("POST %s/chat/completions: %v", p.BaseURL, err)
			}
			if code == http.StatusNotFound {
				alt, err := probe(client, http.MethodGet, p.BaseURL+"/models")
				if err != nil {
					t.Fatalf("GET %s/models after a 404: %v", p.BaseURL, err)
				}
				if alt == http.StatusNotFound {
					t.Errorf("both POST /chat/completions and GET /models return 404 at %s.\n"+
						"The host is alive but neither OpenAI-compatible path is there, which "+
						"usually means the BaseURL points at the provider's native API rather "+
						"than its compatibility endpoint. Check it against the provider's docs "+
						"before changing anything — a 404 here is a lead, not a verdict.",
						p.BaseURL)
					return
				}
				t.Logf("POST 404 but GET /models = %d — gateway routes by method; treated as alive", alt)
				return
			}
			if code >= 500 {
				t.Errorf("%s returned %d — provider-side error, retry before acting on it", p.BaseURL, code)
			}
			// 400/401/403 all mean a real API parsed the request. That is all this
			// test claims; see the file comment on why it cannot claim more.
			t.Logf("%s -> %d", p.BaseURL, code)
		})
	}
}

// probe sends a credential-free request and returns the status code. The body
// is a minimal well-formed chat request so that providers which validate before
// authenticating produce 400 rather than a parse error.
func probe(client *http.Client, method, endpoint string) (int, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"model":"probe","messages":[{"role":"user","content":"hi"}]}`)
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// skipReason returns why a preset is not probed, or "" if it should be.
func skipReason(p ProviderPreset) string {
	if unsubstitutedPlaceholder(p.BaseURL) != "" {
		// cloudflare's {account_id}: there is no host to resolve until a user
		// substitutes one, and TestNoPresetSilentlyShipsAPlaceholder already
		// guarantees such a preset cannot resolve into a provider.
		return "unsubstituted placeholder"
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return ""
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		// ollama and lmstudio are local by design; their absence is not a defect.
		return "local provider"
	}
	return ""
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("unparseable BaseURL %q: %v", raw, err)
	}
	return u.Hostname()
}
