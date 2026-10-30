package runtimeplatform

import (
	"bytes"
	"encoding/json"
)

// QualifiedExecutorIdentity recognizes the adapter's exact immutable artifact
// shape. Recognition alone grants no authority: Core must additionally verify
// the revision binding, qualification lifecycle and current artifact bytes.
func QualifiedExecutorIdentity(adapter, protocol string) bool {
	var a, p map[string]string
	if json.Unmarshal([]byte(adapter), &a) != nil || json.Unmarshal([]byte(protocol), &p) != nil || len(a) != 2 || len(p) != 2 {
		return false
	}
	aa, _ := json.Marshal(a)
	pp, _ := json.Marshal(p)
	if !bytes.Equal(aa, []byte(adapter)) || !bytes.Equal(pp, []byte(protocol)) || a["adapterId"] != "devbridge-codex" || !ValidDigest(a["adapterImplementationDigest"]) || !ValidDigest(p["protocolSchemaDigest"]) {
		return false
	}
	return p["protocolId"] == "codex-app-server/codex-cli 0.149.0" || p["protocolId"] == "codex-app-server/codex-cli 0.159.2"
}
