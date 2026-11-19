package httpapi

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPIMatchesTheRoutes keeps docs/openapi.yaml honest: every route
// registered in server.go must be described there, and the spec must not
// describe routes that do not exist.
func TestOpenAPIMatchesTheRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	// Patterns such as "GET /api/clips/{id}/video.mp4".
	re := regexp.MustCompile(`"(GET|POST|PUT|DELETE|PATCH) (/api/[^"]*)"`)
	routes := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		routes[m[1]+" "+m[2]] = true
	}
	if len(routes) < 20 {
		t.Fatalf("found only %d routes in server.go; is the pattern still right?", len(routes))
	}
	// The media engine's endpoints are one catch-all route; the spec
	// documents the one the client uses.
	routes["POST /media/api/webrtc"] = true

	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("docs/openapi.yaml: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.1") {
		t.Errorf("openapi = %q, want 3.1.x", spec.OpenAPI)
	}
	documented := map[string]bool{}
	ids := map[string]string{}
	for path, item := range spec.Paths {
		for method, v := range item {
			op, isOp := v.(map[string]any)
			if method == "parameters" || !isOp {
				continue
			}
			key := strings.ToUpper(method) + " " + path
			documented[key] = true
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s has no operationId", key)
			} else if other, dup := ids[id]; dup {
				t.Errorf("operationId %q is used by %s and %s", id, other, key)
			}
			ids[id] = key
		}
	}

	var missing, extra []string
	for r := range routes {
		if !documented[r] {
			missing = append(missing, r)
		}
	}
	for r := range documented {
		if !routes[r] {
			extra = append(extra, r)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, r := range missing {
		t.Errorf("route %s is not in docs/openapi.yaml", r)
	}
	for _, r := range extra {
		t.Errorf("docs/openapi.yaml describes %s, which the server does not have", r)
	}
}

// TestOpenAPIReferencesResolve checks every "$ref" points at something the
// spec defines, which a YAML parser alone does not catch.
func TestOpenAPIReferencesResolve(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	n := 0
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				n++
				if !resolves(doc, ref) {
					t.Errorf("$ref %q does not resolve", ref)
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)
	if n == 0 {
		t.Fatal("no $ref found")
	}
}

func resolves(doc map[string]any, ref string) bool {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var cur any = doc
	for _, part := range strings.Split(path, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[part]; !ok {
			return false
		}
	}
	return true
}
