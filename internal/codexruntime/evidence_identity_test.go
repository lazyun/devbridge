package codexruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

func evidenceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validRuntimeEvidence() RuntimeEvidence {
	return RuntimeEvidence{
		Version: 1, ConfigFingerprint: "frozen-config", ConfigDigest: evidenceDigest("config"),
		ResolvedLauncher: "/opt/codex/bin/codex", LauncherDigest: evidenceDigest("launcher"),
		ResolvedNative: "/opt/codex/native/codex", NativeDigest: evidenceDigest("native"), NativeVersion: "0.149.0",
		ProtocolSchemaDigest: evidenceDigest("schema"), Platform: "darwin-25.6-arm64",
		ProfileContentDigest: evidenceDigest("seatbelt-profile"),
		Facts: []EvidenceFact{
			{Dimension: EvidenceSandboxPolicy, Status: EvidenceProven, Source: "OFFLINE_SANDBOX_PROBE", DetailDigest: evidenceDigest("sandbox")},
			{Dimension: EvidenceProtocolSchema, Status: EvidenceProven, Source: "GENERATED_SCHEMA", DetailDigest: evidenceDigest("schema-fact")},
			{Dimension: EvidenceNativeBinary, Status: EvidenceProven, Source: "OFFLINE_BINARY_INSPECTION", DetailDigest: evidenceDigest("native-fact")},
		},
	}
}

func expectation(record RuntimeEvidence, dimensions ...EvidenceDimension) EvidenceExpectation {
	return EvidenceExpectation{IdentityDigest: record.IdentityDigest, ConfigFingerprint: "frozen-config", ConfigDigest: evidenceDigest("config"), RequiredDimensions: dimensions}
}

func TestRuntimeEvidenceIdentityIsCanonicalAndDetached(t *testing.T) {
	input := validRuntimeEvidence()
	first, err := NewRuntimeEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Facts[0].Source = "mutated"
	if first.Facts[0].Source == "mutated" || first.Facts[2].Source == "mutated" {
		t.Fatalf("record aliases caller facts: %+v", first.Facts)
	}
	reversed := validRuntimeEvidence()
	reversed.Facts[0], reversed.Facts[2] = reversed.Facts[2], reversed.Facts[0]
	second, err := NewRuntimeEvidence(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdentityDigest != second.IdentityDigest || !reflect.DeepEqual(first.Facts, second.Facts) {
		t.Fatalf("canonical evidence differs: first=%+v second=%+v", first, second)
	}
	if err := first.Verify(expectation(first, EvidenceSandboxPolicy, EvidenceProtocolSchema)); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
}

func TestRuntimeEvidenceRejectsIncompleteAndTamperedIdentity(t *testing.T) {
	missing := validRuntimeEvidence()
	missing.ProfileContentDigest = ""
	if _, err := NewRuntimeEvidence(missing); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("missing profile digest error = %v", err)
	}
	record, err := NewRuntimeEvidence(validRuntimeEvidence())
	if err != nil {
		t.Fatal(err)
	}
	frozen := record
	record.NativeVersion = "0.150.0"
	if err := record.Verify(expectation(frozen)); !errors.Is(err, ErrEvidenceMismatch) {
		t.Fatalf("tampered record error = %v", err)
	}
	if err := (&record).Verify(EvidenceExpectation{IdentityDigest: frozen.IdentityDigest, ConfigFingerprint: "other", ConfigDigest: evidenceDigest("config")}); !errors.Is(err, ErrEvidenceMismatch) {
		t.Fatalf("config mismatch error = %v", err)
	}
}

func TestRuntimeEvidenceRejectsSameConfigRemintedRuntime(t *testing.T) {
	frozen, err := NewRuntimeEvidence(validRuntimeEvidence())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RuntimeEvidence){
		func(value *RuntimeEvidence) { value.NativeDigest = evidenceDigest("different-native") },
		func(value *RuntimeEvidence) { value.ProtocolSchemaDigest = evidenceDigest("different-schema") },
		func(value *RuntimeEvidence) { value.ProfileContentDigest = evidenceDigest("different-profile") },
	} {
		candidate := validRuntimeEvidence()
		mutate(&candidate)
		reminted, buildErr := NewRuntimeEvidence(candidate)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		if verifyErr := reminted.Verify(expectation(frozen)); !errors.Is(verifyErr, ErrEvidenceMismatch) {
			t.Fatalf("same-config reminted record error = %v", verifyErr)
		}
	}
}

func TestRuntimeEvidenceRequiredDimensionsFailClosed(t *testing.T) {
	for _, status := range []EvidenceStatus{EvidenceUnknown, EvidenceDenied} {
		t.Run(string(status), func(t *testing.T) {
			value := validRuntimeEvidence()
			value.Facts[0].Status = status
			record, err := NewRuntimeEvidence(value)
			if err != nil {
				t.Fatal(err)
			}
			err = record.Verify(expectation(record, EvidenceSandboxPolicy))
			if status == EvidenceUnknown && !errors.Is(err, ErrEvidenceUnknown) {
				t.Fatalf("unknown error = %v", err)
			}
			if status == EvidenceDenied && !errors.Is(err, ErrEvidenceDenied) {
				t.Fatalf("denied error = %v", err)
			}
		})
	}
}

func TestRuntimeEvidenceRefusesClientBooleanSubstitute(t *testing.T) {
	value := validRuntimeEvidence()
	value.Facts = value.Facts[1:] // a client cannot imply sandbox proof with a boolean elsewhere.
	record, err := NewRuntimeEvidence(value)
	if err != nil {
		t.Fatal(err)
	}
	err = record.Verify(expectation(record, EvidenceSandboxPolicy))
	if !errors.Is(err, ErrEvidenceUnknown) {
		t.Fatalf("missing sandbox evidence error = %v", err)
	}
}

func TestRuntimeEvidenceRejectsUnknownDimensionsAndRelativeBinaries(t *testing.T) {
	unknown := validRuntimeEvidence()
	unknown.Facts[0].Dimension = EvidenceDimension("CLIENT_SUPPORTED")
	if _, err := NewRuntimeEvidence(unknown); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("unknown dimension error = %v", err)
	}
	relative := validRuntimeEvidence()
	relative.ResolvedNative = "codex"
	if _, err := NewRuntimeEvidence(relative); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("relative native path error = %v", err)
	}
	valid, err := NewRuntimeEvidence(validRuntimeEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Verify(EvidenceExpectation{IdentityDigest: valid.IdentityDigest, ConfigFingerprint: "frozen-config", ConfigDigest: evidenceDigest("config"), RequiredDimensions: []EvidenceDimension{EvidenceDimension("CLIENT_SUPPORTED")}}); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("unknown required dimension error = %v", err)
	}
}

func TestG0DeniedTemporaryFixtureCannotCertifyProductionIsolation(t *testing.T) {
	value := validRuntimeEvidence()
	value.NativeVersion = "0.149.0"
	value.NativeDigest = "f4a74117b8142cda581c95ff753abf4508b5636d89682c1ed77e4a9249af8963"
	value.LauncherDigest = "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477"
	value.Facts[0] = EvidenceFact{Dimension: EvidenceSandboxPolicy, Status: EvidenceDenied, Source: "OFFLINE_SANDBOX_PROBE", DetailDigest: evidenceDigest("/tmp remains reachable")}
	record, err := NewRuntimeEvidence(value)
	if err != nil {
		t.Fatal(err)
	}
	err = record.Verify(expectation(record, EvidenceSandboxPolicy))
	if !errors.Is(err, ErrEvidenceDenied) {
		t.Fatalf("denied G0 isolation error = %v", err)
	}
}
