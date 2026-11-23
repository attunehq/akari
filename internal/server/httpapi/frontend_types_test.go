package httpapi

import (
	"bytes"
	"os"
	"testing"

	"github.com/jssblck/akari/internal/server/httpapi/tsgen"
)

// TestFrontendAPITypesAreCurrent keeps the frontend's TypeScript types in step
// with the OpenAPI contract: a spec change without regenerated types fails here.
func TestFrontendAPITypesAreCurrent(t *testing.T) {
	const path = "../../../frontend/src/api.generated.ts"
	want, err := tsgen.Generate(openAPIDocument)
	if err != nil {
		t.Fatalf("generate frontend API types: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed frontend API types: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run `go generate ./internal/server/httpapi` and commit the result", path)
	}
}
