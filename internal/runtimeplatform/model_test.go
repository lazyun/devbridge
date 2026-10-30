package runtimeplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCanonicalHashSortsNestedKeysWithoutLosingIntegerPrecision(t *testing.T) {
	// Above 2^53: decoding JSON through float64 silently changes this identity.
	value := struct {
		Z int64          `json:"z"`
		A map[string]any `json:"a"`
	}{Z: 9007199254740993, A: map[string]any{"z": "测量", "a": 7}}
	sum := sha256.Sum256([]byte(`{"a":{"a":7,"z":"测量"},"z":9007199254740993}`))
	if got, want := canonicalHash(value), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("canonical identity = %s, want %s", got, want)
	}
}

func TestProductionIdentityEncodingVectors(t *testing.T) {
	// Fixed digests were calculated independently from the lowerCamel production
	// schema, with sorted JSON keys and evidenceHash set to the empty string.
	// The Skeleton witness's Go struct serialization is not this wire encoding.
	evidence := CompatibilityEvidence{
		DescriptorID: "descriptor-1", PlatformLineageID: "lineage-1", RuntimeRole: "IMPLEMENTATION",
		AdapterImplementationDigest: strings.Repeat("a", 64), ProtocolSchemaDigest: strings.Repeat("b", 64),
		CompatibilityPolicyVersion: "policy-1", MeasuredCompatibility: true,
		CollectorIdentity: "independent-test-collector", CollectionArtifactHash: strings.Repeat("d", 64),
		Source: SourceCoreCompatibilityCollection,
	}
	qualification := QualificationInput{
		QualificationID: "qualification-1", PlatformLineageID: "lineage-1", Generation: 1,
		DescriptorID: "descriptor-1", CompatibilityEvidenceHash: strings.Repeat("e", 64),
		CompatibilityPolicyVersion: "policy-1", IssuanceRequestID: "issue-1",
	}
	invalidation := InvalidationInput{QualificationID: "qualification-1", Generation: 1, Reason: "policy revoked", RequestID: "invalidate-1"}
	for _, tc := range []struct{ name, got, want string }{
		{"evidence", evidence.ComputeHash(), "e260da23928be2ae5a9c0e9678bd3c3a506603954fac4712fe8d4afb56c522c1"},
		{"qualification", qualification.ComputePayloadHash(), "3e3d16ddf38fc5bd012e00a12e40d4c7a0bd3d6fe218e69a40522cc560c7a55f"},
		{"invalidation", invalidation.ComputePayloadHash(), "292cd192621e6267c1fa3ca6ad4966a05cb3d42c46e9406694f287cf572620ac"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("identity encoding changed: got %s, want %s", tc.got, tc.want)
			}
		})
	}
}

func TestIdentityRejectsInvalidUTF8BeforeJSONReplacement(t *testing.T) {
	// encoding/json replaces invalid UTF-8 with U+FFFD. Accepting such IDs
	// would collapse distinct input bytes into the same canonical identity.
	for _, text := range []string{"id-\xff", "id-\xfe", "", " id", "id "} {
		if exactText(text) {
			t.Fatalf("accepted noncanonical identity %q", text)
		}
	}
	if !exactText("平台-1") {
		t.Fatal("valid UTF-8 identity rejected")
	}
	valid := collectionDescriptor()
	valid.DescriptorID = "id-\xff"
	if err := valid.Validate(); err == nil {
		t.Fatal("descriptor accepted invalid UTF-8 identity")
	}
}
