package admin

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEveryRegisteredAdminBrowserRouteIsInOpenAPI(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile("../../../contracts/admin-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(contract, &document); err != nil {
		t.Fatal(err)
	}
	paths, _ := document["paths"].(map[string]any)
	components, _ := document["components"].(map[string]any)
	pathItems, _ := components["pathItems"].(map[string]any)
	re := regexp.MustCompile(`"(GET|POST|PUT|PATCH) (/admin/v1/[^" ]+)"`)
	for _, match := range re.FindAllStringSubmatch(string(source), -1) {
		method, path := strings.ToLower(match[1]), match[2]
		path = strings.Replace(path, "/management-actions/webhook-disable", "/management-actions/{category}", 1)
		path = strings.Replace(path, "/management-actions/api-client-revoke", "/management-actions/{category}", 1)
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("registered route missing from admin OpenAPI: %s %s", match[1], path)
			continue
		}
		if _, ok = item[method]; ok {
			continue
		}
		if ref, refOK := item["$ref"].(string); refOK && strings.HasPrefix(ref, "#/components/pathItems/") {
			resolved, _ := pathItems[strings.TrimPrefix(ref, "#/components/pathItems/")].(map[string]any)
			_, ok = resolved[method]
		}
		if !ok {
			t.Errorf("registered method missing from admin OpenAPI: %s %s", match[1], path)
		}
	}
}

func TestAdminOpenAPIDoesNotAdvertiseDirectDangerousBrowserMutations(t *testing.T) {
	contract, err := os.ReadFile("../../../contracts/admin-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/admin/v1/webhooks/endpoints/{id}/disable:", "/admin/v1/api-clients/{id}/revoke:"} {
		if strings.Contains(string(contract), forbidden) {
			t.Fatalf("browser contract advertises internal dangerous execution path: %s", forbidden)
		}
	}
}
