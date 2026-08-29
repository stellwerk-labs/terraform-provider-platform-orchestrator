package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/require"
)

type automationAPIFixture struct {
	t       *testing.T
	mu      sync.Mutex
	role    map[string]any
	mapping map[string]any
	key     map[string]any

	metadataCreates       int
	metadataUpdates       int
	metadataDeletes       int
	sawMetadataClearPatch bool
}

func (f *automationAPIFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer local-test-token" {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	const (
		rolesPath       = "/orgs/local-test-org/roles"
		permissionsPath = "/orgs/local-test-org/permissions"
		mappingsPath    = "/orgs/local-test-org/scim/group-mappings"
		keysPath        = "/orgs/local-test-org/metadata-keys"
	)

	switch {
	case r.URL.Path == permissionsPath && r.Method == http.MethodGet:
		writeJSON(f.t, w, http.StatusOK, map[string]any{"items": []map[string]any{{
			"id": "environment.read", "display_name": "Read environments", "description": "Read environments",
			"category": "Environment", "level": "read", "scopes": []string{"organization", "project"},
		}}})
	case r.URL.Path == rolesPath:
		f.handleRoles(w, r)
	case strings.HasPrefix(r.URL.Path, rolesPath+"/"):
		f.handleRole(w, r)
	case r.URL.Path == mappingsPath && r.Method == http.MethodGet:
		items := []map[string]any{}
		if f.mapping != nil {
			items = append(items, f.mapping)
		}
		writeJSON(f.t, w, http.StatusOK, map[string]any{"items": items})
	case strings.HasPrefix(r.URL.Path, mappingsPath+"/"):
		f.handleMapping(w, r, strings.TrimPrefix(r.URL.Path, mappingsPath+"/"))
	case r.URL.Path == keysPath:
		f.handleKeys(w, r)
	case strings.HasPrefix(r.URL.Path, keysPath+"/"):
		f.handleKey(w, r, strings.TrimPrefix(r.URL.Path, keysPath+"/"))
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (f *automationAPIFixture) handleRoles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			DisplayName string   `json:"display_name"`
			Permissions []string `json:"permissions"`
		}
		if !f.decodeJSON(w, r, &body) {
			return
		}
		f.role = map[string]any{
			"id": uuid.MustParse("11111111-1111-4111-8111-111111111111"), "display_name": body.DisplayName,
			"permissions": body.Permissions, "is_system": false, "created_at": "2026-08-28T10:00:00Z",
			"created_by": uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		}
		writeJSON(f.t, w, http.StatusCreated, f.role)
	case http.MethodGet:
		items := []map[string]any{}
		if f.role != nil {
			items = append(items, f.role)
		}
		writeJSON(f.t, w, http.StatusOK, map[string]any{"items": items})
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (f *automationAPIFixture) handleRole(w http.ResponseWriter, r *http.Request) {
	if f.role == nil {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(f.t, w, http.StatusOK, f.role)
	case http.MethodPut:
		var body struct {
			DisplayName string   `json:"display_name"`
			Permissions []string `json:"permissions"`
		}
		if !f.decodeJSON(w, r, &body) {
			return
		}
		f.role["display_name"] = body.DisplayName
		f.role["permissions"] = body.Permissions
		writeJSON(f.t, w, http.StatusOK, f.role)
	case http.MethodDelete:
		f.role = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (f *automationAPIFixture) handleMapping(w http.ResponseWriter, r *http.Request, escapedName string) {
	name := escapedName
	switch r.Method {
	case http.MethodPut:
		var body struct {
			RoleID string `json:"role_id"`
		}
		if !f.decodeJSON(w, r, &body) {
			return
		}
		f.mapping = map[string]any{"group_display_name": name, "role_id": body.RoleID, "created_at": "2026-08-28T10:01:00Z"}
		writeJSON(f.t, w, http.StatusOK, f.mapping)
	case http.MethodDelete:
		f.mapping = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (f *automationAPIFixture) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		if !f.decodeJSON(w, r, &body) {
			return
		}
		body["created_at"] = "2026-08-28T10:02:00Z"
		f.key = body
		f.metadataCreates++
		writeJSON(f.t, w, http.StatusCreated, f.key)
	case http.MethodGet:
		items := []map[string]any{}
		if f.key != nil {
			items = append(items, f.key)
		}
		writeJSON(f.t, w, http.StatusOK, map[string]any{"items": items})
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (f *automationAPIFixture) handleKey(w http.ResponseWriter, r *http.Request, _ string) {
	if f.key == nil {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(f.t, w, http.StatusOK, f.key)
	case http.MethodPatch:
		var body map[string]any
		if !f.decodeJSON(w, r, &body) {
			return
		}
		for key, value := range body {
			f.key[key] = value
		}
		f.metadataUpdates++
		description, hasDescription := body["description"]
		schema, hasSchema := body["schema"].(map[string]any)
		format, hasFormat := schema["format"]
		pattern, hasPattern := schema["pattern"]
		if hasDescription && description == nil && hasSchema && hasFormat && format == nil && hasPattern && pattern == nil {
			f.sawMetadataClearPatch = true
		}
		writeJSON(f.t, w, http.StatusOK, f.key)
	case http.MethodDelete:
		f.key = nil
		f.metadataDeletes++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode local API response: %v", err)
	}
}

func (f *automationAPIFixture) decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		f.t.Errorf("decode %s %s request: %v", r.Method, r.URL.Path, err)
		http.Error(w, `{"message":"invalid JSON"}`, http.StatusBadRequest)
		return false
	}
	return true
}

func TestAccAutomationResourcesLocal(t *testing.T) {
	fixture := &automationAPIFixture{t: t}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: automationResourcesConfig(server.URL, "Automation role", "Initial metadata"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("platform-orchestrator_role.test", tfjsonpath.New("id"), knownvalue.StringExact("11111111-1111-4111-8111-111111111111")),
					statecheck.ExpectKnownValue("platform-orchestrator_scim_group_mapping.test", tfjsonpath.New("group_display_name"), knownvalue.StringExact("Platform Engineers")),
					statecheck.ExpectKnownValue("platform-orchestrator_metadata_key.test", tfjsonpath.New("description"), knownvalue.StringExact("Initial metadata")),
					statecheck.ExpectKnownValue("data.platform-orchestrator_permissions.all", tfjsonpath.New("permissions"), knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.platform-orchestrator_roles.all", tfjsonpath.New("roles"), knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.platform-orchestrator_scim_group_mappings.all", tfjsonpath.New("mappings"), knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.platform-orchestrator_metadata_keys.all", tfjsonpath.New("metadata_keys"), knownvalue.ListSizeExact(1)),
				},
			},
			{
				Config: automationResourcesConfig(server.URL, "Updated automation role", "Updated metadata"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("platform-orchestrator_role.test", tfjsonpath.New("display_name"), knownvalue.StringExact("Updated automation role")),
					statecheck.ExpectKnownValue("platform-orchestrator_metadata_key.test", tfjsonpath.New("description"), knownvalue.StringExact("Updated metadata")),
				},
			},
			{
				Config: automationResourcesConfig(server.URL, "Updated automation role", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("platform-orchestrator_metadata_key.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("platform-orchestrator_metadata_key.test", tfjsonpath.New("pattern"), knownvalue.Null()),
				},
			},
			{
				ResourceName:      "platform-orchestrator_role.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:                         "platform-orchestrator_scim_group_mapping.test",
				ImportState:                          true,
				ImportStateId:                        "Platform Engineers",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "group_display_name",
			},
			{
				ResourceName:                         "platform-orchestrator_metadata_key.test",
				ImportState:                          true,
				ImportStateId:                        "cost-center",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, 1, fixture.metadataCreates, "removing optional fields must update the metadata key in place")
	require.Equal(t, 2, fixture.metadataUpdates)
	require.Equal(t, 1, fixture.metadataDeletes)
	require.True(t, fixture.sawMetadataClearPatch, "provider did not send explicit nulls for removed optional fields")
}

func automationResourcesConfig(apiURL, roleDisplayName, metadataDescription string) string {
	metadataOptionals := ""
	if metadataDescription != "" {
		metadataOptionals = fmt.Sprintf("  description = %q\n  pattern     = \"^[A-Z0-9-]+$\"", metadataDescription)
	}
	return fmt.Sprintf(`
provider "platform-orchestrator" {
  api_url    = %[1]q
  org_id     = "local-test-org"
  auth_token = "local-test-token"
}

resource "platform-orchestrator_role" "test" {
  display_name = %[2]q
  permissions  = ["environment.read"]
}

resource "platform-orchestrator_scim_group_mapping" "test" {
  group_display_name = "Platform Engineers"
  role_id            = platform-orchestrator_role.test.id
}

resource "platform-orchestrator_metadata_key" "test" {
  name        = "cost-center"
  schema_type = "string"
%[3]s
}

data "platform-orchestrator_permissions" "all" {}

data "platform-orchestrator_role" "test" {
  id = platform-orchestrator_role.test.id
}

data "platform-orchestrator_roles" "all" {
  depends_on = [platform-orchestrator_role.test]
}

data "platform-orchestrator_scim_group_mappings" "all" {
  depends_on = [platform-orchestrator_scim_group_mapping.test]
}

data "platform-orchestrator_metadata_key" "test" {
  name = platform-orchestrator_metadata_key.test.name
}

data "platform-orchestrator_metadata_keys" "all" {
  depends_on = [platform-orchestrator_metadata_key.test]
}
`, apiURL, roleDisplayName, metadataOptionals)
}
