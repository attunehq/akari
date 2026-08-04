package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jssblck/akari/internal/server/web"
)

func TestProductSiteRedirectPreservesPathAndQuery(t *testing.T) {
	t.Parallel()

	for _, requestURI := range []string{
		"/",
		"/guide/getting-started",
		"/guide/getting-started.md?source=agent",
		"/llms-full.txt",
		"/og.png",
	} {
		t.Run(requestURI, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, requestURI, nil)

			(&Server{}).handleProductSiteRedirect(recorder, request)

			response := recorder.Result()
			if response.StatusCode != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want 308", response.StatusCode)
			}
			if got, want := response.Header.Get("Location"), web.ProductSiteURL+requestURI; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}
		})
	}
}
