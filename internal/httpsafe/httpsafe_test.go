package httpsafe

import (
	"net/http"
	"testing"
)

func TestCheckRedirect_rejectsDowngradeAndExcessiveHops(t *testing.T) {
	httpsReq, _ := http.NewRequest(http.MethodGet, "https://example.com/b", nil)
	httpReq, _ := http.NewRequest(http.MethodGet, "http://example.com/b", nil)

	// A single https->https hop is allowed.
	if err := CheckRedirect(httpsReq, []*http.Request{httpsReq}); err != nil {
		t.Errorf("https redirect should be allowed: %v", err)
	}
	// A downgrade to http is rejected.
	if err := CheckRedirect(httpReq, []*http.Request{httpsReq}); err == nil {
		t.Error("redirect to http should be rejected")
	}
	// Too many hops are rejected.
	via := make([]*http.Request, MaxRedirects+1)
	for i := range via {
		via[i] = httpsReq
	}
	if err := CheckRedirect(httpsReq, via); err == nil {
		t.Error("excessive redirects should be rejected")
	}
}
