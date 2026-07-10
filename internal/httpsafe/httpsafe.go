// Package httpsafe provides the shared HTTP transport policy for everything
// env-starter downloads and later executes (url sources, self-update
// artifacts): every redirect hop must stay on https — a redirect must never
// downgrade an https request to plaintext http — and redirect chains are
// bounded.
package httpsafe

import (
	"fmt"
	"net/http"
)

// MaxRedirects bounds how many redirect hops a download may follow.
const MaxRedirects = 10

// CheckRedirect is the redirect policy for download requests: every hop must
// stay on https (no downgrade to http) and the chain is bounded.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-https URL %q", req.URL.String())
	}
	if len(via) >= MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	return nil
}

// Client follows only https redirects, up to MaxRedirects hops.
var Client = &http.Client{CheckRedirect: CheckRedirect}
