package provider

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var nativeInventoryDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var nativeInventoryField = regexp.MustCompile(`^[a-z_]+$`)

// Field names are canonical ASCII. encoding/json's case and Unicode folding is
// inappropriate for this public-only input boundary; human-readable values remain Unicode.
func nativeCatalogueKeys(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if !nativeInventoryField.MatchString(key) || !nativeCatalogueKeys(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !nativeCatalogueKeys(child) {
				return false
			}
		}
	}
	return true
}

func nativeCatalogue(raw string) (*cp.RunnerExecutionCatalogue, error) {
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("native inventory exceeds 64 KiB")
	}
	var document any
	if json.Unmarshal([]byte(raw), &document) != nil || !nativeCatalogueKeys(document) {
		return nil, fmt.Errorf("native inventory must use canonical public JSON field names")
	}
	var catalogue cp.RunnerExecutionCatalogue
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalogue) != nil || !nativeCatalogueHeader(catalogue) {
		return nil, fmt.Errorf("native inventory must be a complete public v2 catalogue with a valid digest and tools array")
	}
	for _, tool := range catalogue.Tools {
		if tool.Id == "" || strings.TrimSpace(tool.Name) == "" || !tool.Phase.Valid() || !tool.Retry.Valid() || tool.RequiredCapabilities == nil {
			return nil, fmt.Errorf("native inventory contains incomplete public tool metadata")
		}
	}
	return &catalogue, nil
}

func nativePublicKey(value string) bool {
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return false
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return false
	}
	_, ok := key.(ed25519.PublicKey)
	return ok
}

func nativeCatalogueHeader(c cp.RunnerExecutionCatalogue) bool {
	return c.ContractVersion == cp.StellwerkRunnerCataloguev1 && c.RuntimeVersion == cp.StellwerkRunnerRolloutv2 && nativeInventoryDigest.MatchString(c.ToolsDigest) && c.Tools != nil && len(c.Tools) <= 32
}

func nativeAgentInventory(value jsontypes.Normalized, create bool) (*cp.RunnerExecutionCatalogue, error) {
	if value.IsNull() {
		if create {
			return nil, fmt.Errorf("a new native Runner requires non-null approved inventory")
		}
		return nil, nil
	}
	return nativeCatalogue(value.ValueString())
}

func nativeAgentProtocols(protocols []string) bool {
	return slices.Contains(protocols, "stellwerk.pipeline/v2") && slices.Contains(protocols, "stellwerk.pipeline.plan-apply/v2") && !slices.Contains(protocols, "stellwerk.pipeline/v1") && !slices.Contains(protocols, "stellwerk.pipeline.plan-apply/v1")
}

func nativeAgentDeclarations(ctx context.Context, config nativeAgentRunnerConfiguration, model nativeAgentRunnerModel) ([]string, []string, diag.Diagnostics) {
	var d diag.Diagnostics
	for _, value := range []attr.Value{config.Key, config.Capacity, config.Protocols, config.Capabilities, config.Inventory, model.Description, model.Backend} {
		if value.IsUnknown() {
			d.AddError(PO_INPUT_ERR, "Native Runner inputs must resolve before applying.")
			return nil, nil, d
		}
	}
	if config.Capacity.ValueInt64() < 1 || !nativePublicKey(config.Key.ValueString()) || config.Protocols.IsNull() || config.Capabilities.IsNull() {
		d.AddError(PO_INPUT_ERR, "Native Runner requires a public Ed25519 key, positive capacity and declared protocol/capability sets.")
		return nil, nil, d
	}
	var protocols, capabilities []string
	d.Append(config.Protocols.ElementsAs(ctx, &protocols, false)...)
	d.Append(config.Capabilities.ElementsAs(ctx, &capabilities, false)...)
	if d.HasError() {
		return nil, nil, d
	}
	if !nativeAgentProtocols(protocols) {
		d.AddError(PO_INPUT_ERR, "Native Runner requires v2 pipeline and saved-plan protocols, without legacy v1 variants.")
	}
	return protocols, append([]string{}, capabilities...), d
}

func nativeAgentInput(ctx context.Context, model nativeAgentRunnerModel, create bool) (cp.RunnerConfiguration, diag.Diagnostics) {
	var result cp.RunnerConfiguration
	if len(model.ID.ValueString()) > 128 || !nativeRunnerIDPattern.MatchString(model.ID.ValueString()) {
		var d diag.Diagnostics
		d.AddError(PO_INPUT_ERR, "Native v2 Runner ID must resolve to 2–128 characters matching the Core Runner pattern.")
		return result, d
	}
	config, d := nativeAgentConfig(ctx, model.Configuration)
	if d.HasError() {
		return result, d
	}
	protocols, capabilities, e := nativeAgentDeclarations(ctx, config, model)
	d.Append(e...)
	if d.HasError() {
		return result, d
	}
	inventory, err := nativeAgentInventory(config.Inventory, create)
	if err != nil {
		d.AddError(PO_INPUT_ERR, err.Error())
		return result, d
	}
	err = result.FromNativeAgentRunnerConfiguration(cp.NativeAgentRunnerConfiguration{Type: cp.RunnerTypeNativeAgent, Key: config.Key.ValueString(), MaxParallelJobs: int(config.Capacity.ValueInt64()), SupportedProtocols: &protocols, Capabilities: &capabilities, NativeExecution: inventory})
	if err != nil {
		d.AddError(PO_INPUT_ERR, "Native Runner registration could not be encoded.")
	}
	return result, d
}
