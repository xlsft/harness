package localmodels

import (
	"encoding/json"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// PolicyKey is the key of the org-scope POLICY document that disallows local models
// (harness.auth.v1.AdminService.SetLocalModelPolicy writes it).
const PolicyKey = "disableLocalModels"

// PolicyDisabledIn reports whether any org-scope POLICY document disallows local models: "disableLocalModels"
// is true or "disable".
func PolicyDisabledIn(docs []*syncv1.Document) bool {
	for _, d := range docs {
		if d.GetScope() != commonv1.Scope_SCOPE_ORG {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(d.GetContent()), &raw) != nil {
			continue
		}
		var b bool
		if json.Unmarshal(raw[PolicyKey], &b) == nil && b {
			return true
		}
		var s string
		if json.Unmarshal(raw[PolicyKey], &s) == nil && s == "disable" {
			return true
		}
	}
	return false
}
