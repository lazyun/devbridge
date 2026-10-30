package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VerifyCore prevents AVAILABLE against an attached old Core, a different
// Desktop configuration, or a missing/non-executable selection after probing.
func VerifyCore(endpoint, selectionFile string, result Result) Result {
	if result.Status != Available {
		return result
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(strings.TrimRight(endpoint, "/") + "/v1/runtime/codex")
	var actual struct {
		SelectionFile string `json:"selectionFile"`
		Executable    string `json:"executable"`
		Error         string `json:"error"`
	}
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = fmt.Errorf("Core returned HTTP %d", response.StatusCode)
		} else {
			err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&actual)
		}
	}
	if err == nil && actual.Error != "" {
		err = errors.New(actual.Error)
	}
	if err == nil && (actual.SelectionFile != selectionFile || actual.Executable != result.Executable) {
		err = fmt.Errorf("probe selected %q (%s), Core selected %q (%s)", result.Executable, selectionFile, actual.Executable, actual.SelectionFile)
	}
	if err != nil {
		result.Status = Error
		result.Error = "CODEX_EXECUTABLE_MISMATCH: " + err.Error()
	}
	return result
}
