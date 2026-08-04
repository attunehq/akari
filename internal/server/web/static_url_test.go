package web

import (
	"context"
	"strings"
	"testing"
)

func TestStaticURLFingerprintsEmbeddedAsset(t *testing.T) {
	ctx := context.Background()
	got := string(StaticURL(ctx, "css/base.css"))
	if !strings.HasPrefix(got, "/static/css/base.css?v=") {
		t.Fatalf("StaticURL() = %q, want fingerprinted base URL", got)
	}
	if got != string(StaticURL(ctx, "/css/base.css")) {
		t.Fatal("StaticURL should accept an optional leading slash")
	}
}

func TestStaticURLCarriesBasePath(t *testing.T) {
	ctx := WithBasePath(context.Background(), "/proxy/akari")
	got := string(StaticURL(ctx, "css/base.css"))
	if !strings.HasPrefix(got, "/proxy/akari/static/css/base.css?v=") {
		t.Fatalf("StaticURL() = %q, want prefixed fingerprinted URL", got)
	}
}

func TestPublicErrorUsesOnlyFingerprintedStaticStyles(t *testing.T) {
	html := renderComponent(t, PublicErrorPage(404, "That page does not exist."))
	for _, asset := range []string{"css/base.css", "css/layout.css"} {
		if want := `href="/static/` + asset + `?v=`; !strings.Contains(html, want) {
			t.Errorf("public error layout is missing %q", asset)
		}
	}
	for _, retired := range []string{"htmx", "charts.js", "app.js", "insights.js"} {
		if strings.Contains(html, retired) {
			t.Errorf("public error layout still ships retired runtime %q", retired)
		}
	}
}

func TestStaticURLRejectsMissingAsset(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("StaticURL should panic for an asset that cannot be served")
		}
	}()
	StaticURL(context.Background(), "js/missing.js")
}
