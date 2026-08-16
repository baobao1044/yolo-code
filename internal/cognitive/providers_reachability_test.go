// The registry's presets were never checked against the networks they name.
//
// Everything guarding providerPresets before this file was structural: fields
// are non-empty (TestProviderPresetsHaveRequiredFields), keyed presets name a
// KeyEnv (TestEveryKeyedPresetHasKeyEnv), the list is sorted, no placeholder
// resolves (TestNoPresetSilentlyShipsAPlaceholder). Every one of those passes
// for a preset pointing at a host that has not existed for a year. The registry
// is a table of claims about the outside world and the suite only checked that
// the table was well-formed.
//
// That gap has a specific cost. A wrong BaseURL or DefaultModel fails at the
// first live request and nowhere earlier, so the user pays for it — the failure
// surfaces as a network error in their terminal, mid-task, after they have
// exported an API key and picked a provider that the tool advertised.
//
// This file closes the half that can be closed offline. The other half is
// providers_live_test.go, behind -tags=providercheck, which actually resolves
// every host; it cannot run in CI, which is precisely why the findings it made
// are pinned here as data.

package cognitive

import (
	"net/url"
	"strings"
	"testing"
)

// deadHost records a host that a preset pointed at and that does not resolve.
//
// Not "deprecated", not "returns errors" — no address record at all, so the
// client cannot open a connection and the DefaultModel beside it never gets a
// chance to be wrong. Both entries were found by resolving every preset host on
// the date below; both had been shipping in the registry.
type deadHost struct {
	host     string
	preset   string
	evidence string
}

var hostsConfirmedDead = []deadHost{
	{
		host:   "api-inference.huggingface.co",
		preset: "huggingface",
		evidence: "NXDOMAIN on 2026-08-16. HuggingFace moved OpenAI-compatible " +
			"inference to router.huggingface.co/v1 (GET /models returns 200 there). " +
			"The preset's DefaultModel was checked against that live list and is " +
			"correct — meta-llama/Llama-3.3-70B-Instruct is present — so only the " +
			"BaseURL was wrong, and swapping the host repaired the preset whole.",
	},
	{
		host:   "api.lepton.ai",
		preset: "lepton",
		evidence: "NXDOMAIN on 2026-08-16. Lepton AI was absorbed into NVIDIA DGX " +
			"Cloud Lepton: lepton.ai now 301s to nvidia.com and the API host was " +
			"withdrawn with it. There is no drop-in successor URL — the replacement " +
			"is an enterprise GPU platform, not an OpenAI-compatible endpoint — so " +
			"the preset was removed rather than repointed.",
	},
}

// TestNoPresetPointsAtAHostConfirmedDead is the regression guard. It is a
// deliberately small claim: these two hosts are known not to resolve, and no
// preset may name one. It cannot notice a third host dying — only
// providers_live_test.go can do that, and only when someone runs it — but it
// does stop a revert or a copy-paste from a stale example reintroducing one of
// the two that were found.
func TestNoPresetPointsAtAHostConfirmedDead(t *testing.T) {
	for _, p := range ListProviders() {
		u, err := url.Parse(p.BaseURL)
		if err != nil {
			t.Errorf("preset %q has an unparseable BaseURL %q: %v", p.Name, p.BaseURL, err)
			continue
		}
		for _, dead := range hostsConfirmedDead {
			if u.Hostname() != dead.host {
				continue
			}
			t.Errorf("preset %q points at %s, which does not resolve.\n\n%s\n\n"+
				"A preset naming this host cannot make a request at all: the failure "+
				"is DNS, before TLS, before auth, before the model name matters. "+
				"Nothing else in this package's tests can see that, because they all "+
				"check the shape of the table rather than what it points at. If this "+
				"host has genuinely come back, delete its entry from hostsConfirmedDead "+
				"and say so — do not silence the test by renaming the preset.",
				p.Name, dead.host, dead.evidence)
		}
	}
}

// TestEveryPresetBaseURLIsAWellFormedAbsoluteURL is the cheap structural check
// that should have existed alongside the non-empty assertion. `BaseURL != ""`
// accepts "https://api.example.com/v1 " and "api.example.com/v1"; the first
// fails at request time with an opaque error and the second silently becomes a
// relative path.
func TestEveryPresetBaseURLIsAWellFormedAbsoluteURL(t *testing.T) {
	for _, p := range ListProviders() {
		if trimmed := strings.TrimSpace(p.BaseURL); trimmed != p.BaseURL {
			t.Errorf("preset %q BaseURL has surrounding whitespace: %q", p.Name, p.BaseURL)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil {
			t.Errorf("preset %q BaseURL %q is unparseable: %v", p.Name, p.BaseURL, err)
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Errorf("preset %q BaseURL %q has scheme %q, want http or https",
				p.Name, p.BaseURL, u.Scheme)
		}
		if u.Hostname() == "" {
			t.Errorf("preset %q BaseURL %q has no host", p.Name, p.BaseURL)
		}
		if strings.HasSuffix(p.BaseURL, "/") {
			t.Errorf("preset %q BaseURL %q has a trailing slash; the client appends "+
				"/chat/completions and would build a double slash", p.Name, p.BaseURL)
		}
	}
}
