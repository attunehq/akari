// Command tsgen writes the frontend's TypeScript API types from the OpenAPI
// document. It runs through `go generate ./internal/server/httpapi`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jssblck/akari/internal/server/httpapi/tsgen"
)

func main() {
	in := flag.String("in", "", "OpenAPI document to read")
	out := flag.String("out", "", "TypeScript file to write")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: tsgen -in openapi.json -out api.generated.ts")
		os.Exit(2)
	}
	document, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
	generated, err := tsgen.Generate(document)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, generated, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
}
