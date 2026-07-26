package store_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/jssblck/akari/internal/server/storetest"
)

// findNilContainers walks v and reports the dotted path of every nil slice or map it
// reaches. It is the enforcement behind the "a read returns an empty container, never a
// nil one" rule: encoding/json renders both as null, so one nil field here is what makes
// an array or object in the browser contract nullable, and every consumer pay for it.
func findNilContainers(v reflect.Value, path string, out *[]string) {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			*out = append(*out, path)
			return
		}
		for i := 0; i < v.Len(); i++ {
			findNilContainers(v.Index(i), path+"[]", out)
		}
	case reflect.Map:
		if v.IsNil() {
			*out = append(*out, path)
			return
		}
		for _, k := range v.MapKeys() {
			findNilContainers(v.MapIndex(k), path+"["+k.String()+"]", out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			findNilContainers(v.Field(i), path+"."+f.Name, out)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			findNilContainers(v.Elem(), path, out)
		}
	}
}

// TestEmptyScopeReadsCarryNoNilContainers pins the boundary rule on the hardest input:
// a scope with no sessions at all, which is where a constructor that only fills
// containers from rows leaves them nil. Every array and object the browser sees is
// declared non-nullable, so a nil here is a contract violation, not a cosmetic
// difference.
func TestEmptyScopeReadsCarryNoNilContainers(t *testing.T) {
	t.Parallel()
	st := storetest.NewStore(t)
	ctx := context.Background()

	f := store.AnalyticsFilter{Since: time.Now().Add(-24 * time.Hour), Bucket: "day"}

	reads := []struct {
		name string
		get  func() (any, error)
	}{
		{"Analytics", func() (any, error) { return st.Analytics(ctx, f) }},
		{"Insights", func() (any, error) { return st.Insights(ctx, f, store.AllInsightsPanels) }},
		{"ListProjects", func() (any, error) { return st.ListProjects(ctx) }},
		{"WindowSessionPage", func() (any, error) {
			return st.WindowSessionPage(ctx, store.SessionFilter{Limit: 20})
		}},
		{"SessionFacets", func() (any, error) { return st.SessionFacets(ctx, 0) }},
		{"GlobalFacets", func() (any, error) { return st.GlobalFacets(ctx) }},
	}
	for _, r := range reads {
		t.Run(r.name, func(t *testing.T) {
			got, err := r.get()
			if err != nil {
				t.Fatalf("%s: %v", r.name, err)
			}
			var nils []string
			findNilContainers(reflect.ValueOf(got), r.name, &nils)
			if len(nils) > 0 {
				t.Errorf("nil containers on an empty scope:\n\t%s", strings.Join(nils, "\n\t"))
			}
		})
	}
}
