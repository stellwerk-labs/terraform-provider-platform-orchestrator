package provider

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

type platformAPIError struct {
	Code    string         `json:"error"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func addAPIResponseError(diagnostics *diag.Diagnostics, action string, status int, body []byte) {
	problem := platformAPIError{}
	if err := json.Unmarshal(body, &problem); err == nil && (problem.Code != "" || problem.Message != "") {
		title := PO_API_ERR
		if len(problem.Code) > len("plugin_") && problem.Code[:len("plugin_")] == "plugin_" {
			title = "Required Stellwerk plugin unavailable"
		}
		message := problem.Message
		if message == "" {
			message = problem.Code
		}
		diagnostics.AddError(title, fmt.Sprintf("%s failed with HTTP %d (%s): %s", action, status, problem.Code, message))
		return
	}
	diagnostics.AddError(PO_API_ERR, fmt.Sprintf("%s failed with HTTP %d: %s", action, status, string(body)))
}
