// Package mcpmailbox is an acceptance-harness journal, not a production worker.
// It conservatively refuses automatic redispatch after any recorded dispatch
// attempt whose response was not durably retained. It cannot establish a remote
// business outcome: callers must read the original Core receipt separately.
package mcpmailbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrOutcomeUnknown = errors.New("mailbox dispatch outcome unknown; inspect original Core identity, never automatically resend")
	ErrInputConflict  = errors.New("mailbox label already bound to different request bytes")
)
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Journal struct{ Root string }
type Record struct {
	RequestSHA256 string `json:"requestSha256"`
	// Response is the actual transport observation, not proof of business success.
	Response []byte `json:"response,omitempty"`
}

func digest(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeExclusive(path string, b []byte) (bool, error) {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	// Never remove a claim on any write/sync failure: dispatch certainty is lost.
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = syncDir(filepath.Dir(path))
	}
	return true, e
}
func readRecord(path, hash string, response bool) (Record, error) {
	var r Record
	b, e := os.ReadFile(path)
	if e != nil {
		if os.IsNotExist(e) {
			return r, e
		}
		return r, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&r); e != nil || !json.Valid(b) {
		return r, fmt.Errorf("%w: unreadable mailbox journal", ErrOutcomeUnknown)
	}
	if len(r.RequestSHA256) != 64 {
		return r, fmt.Errorf("%w: invalid request digest", ErrOutcomeUnknown)
	}
	if _, e = hex.DecodeString(r.RequestSHA256); e != nil {
		return r, fmt.Errorf("%w: invalid request digest", ErrOutcomeUnknown)
	}
	if response && !json.Valid(r.Response) || !response && len(r.Response) != 0 {
		return r, fmt.Errorf("%w: invalid mailbox record shape", ErrOutcomeUnknown)
	}
	if r.RequestSHA256 != hash {
		return r, ErrInputConflict
	}
	return r, nil
}

// Deliver sends only after an exclusive durable claim. Reopening a claimed
// request never invokes send, even if the first process crashed before send.
// A persisted response is returned unchanged without invoking send again.
func (j Journal) Deliver(label string, request []byte, send func() ([]byte, error)) ([]byte, error) {
	if !labelPattern.MatchString(label) || send == nil {
		return nil, errors.New("valid label and sender required")
	}
	if e := os.MkdirAll(j.Root, 0700); e != nil {
		return nil, e
	}
	hash := digest(request)
	claim := filepath.Join(j.Root, label+".claim.json")
	result := filepath.Join(j.Root, label+".response.json")
	if r, e := readRecord(result, hash, true); e == nil {
		return append([]byte(nil), r.Response...), nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	raw, _ := json.Marshal(Record{RequestSHA256: hash})
	fresh, e := writeExclusive(claim, raw)
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	if !fresh {
		if _, e = readRecord(claim, hash, false); e != nil {
			return nil, e
		}
		// A concurrent first sender may have completed since our first response read.
		if r, e := readRecord(result, hash, true); e == nil {
			return append([]byte(nil), r.Response...), nil
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		return nil, ErrOutcomeUnknown
	}
	response, e := send()
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	if !json.Valid(response) {
		return nil, fmt.Errorf("%w: response is not JSON", ErrOutcomeUnknown)
	}
	data, e := json.Marshal(Record{RequestSHA256: hash, Response: response})
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	// Exclusive result publication: incomplete response bytes remain unavailable,
	// never accepted as a completed response. Unknown is conservative on any error.
	temp, e := os.CreateTemp(j.Root, ".response-*")
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	tmp := temp.Name()
	defer os.Remove(tmp)
	if _, e = temp.Write(data); e == nil {
		e = temp.Sync()
	}
	closeErr := temp.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(tmp, result)
	}
	if e == nil {
		e = syncDir(j.Root)
	}
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
	}
	return append([]byte(nil), response...), nil
}
