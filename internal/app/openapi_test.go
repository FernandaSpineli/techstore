package app_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestOpenAPIMatchesRoutes keeps docs/openapi.yaml in step with the router:
// every registered API route is documented and every documented operation
// exists.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	routesSrc, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	norm := func(method, path string) string { return method + " " + param.ReplaceAllString(path, "{}") }

	var inCode []string
	for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\("(GET|POST|PUT|PATCH|DELETE) (/api/[^"]+|/healthz|/readyz)"`).
		FindAllStringSubmatch(string(routesSrc), -1) {
		inCode = append(inCode, norm(m[1], m[2]))
	}

	// Path items are indented two spaces under "paths:", operations four.
	var inSpec []string
	var path string
	section := strings.SplitN(string(spec), "\ncomponents:", 2)[0]
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "  /"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case path != "" && regexp.MustCompile(`^    (get|post|put|patch|delete):`).MatchString(line):
			inSpec = append(inSpec, norm(strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":")), path))
		}
	}

	slices.Sort(inCode)
	slices.Sort(inSpec)
	for _, r := range inCode {
		if !slices.Contains(inSpec, r) {
			t.Errorf("route %s is not documented in docs/openapi.yaml", r)
		}
	}
	for _, r := range inSpec {
		if !slices.Contains(inCode, r) {
			t.Errorf("docs/openapi.yaml documents %s, which the router does not serve", r)
		}
	}
	if len(inCode) < 30 {
		t.Fatalf("only %d routes found; the route pattern no longer matches routes.go", len(inCode))
	}
}
