package review

import "time"

// FileEvidence is the content-free durable proof returned to an Upper LLM for
// use in Report evidenceRefs. Digest identifies the exact bytes it observed.
type FileEvidence struct {
	EvidenceID string `json:"evidenceId"`
	// RepositoryID and MemberID are populated for protocol v2.  They make an
	// evidence reference unambiguous when two repositories contain the same
	// relative path and bytes.
	RepositoryID  string    `json:"repositoryId,omitempty"`
	MemberID      string    `json:"memberId,omitempty"`
	MemberVersion int       `json:"memberVersion,omitempty"`
	Path          string    `json:"path"`
	Digest        string    `json:"digest"`
	ByteCount     int64     `json:"byteCount"`
	LineCount     int       `json:"lineCount"`
	RecordedAt    time.Time `json:"recordedAt"`
}
