package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/repository"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type RepositoryFileReadInput struct {
	AISessionID           string `json:"aiSessionId"`
	RepositoryID          string `json:"repositoryId"`
	Path                  string `json:"path"`
	Commit                string `json:"commit,omitempty"`
	Offset                int64  `json:"offset,omitempty"`
	MaxBytes              int    `json:"maxBytes,omitempty"`
	ExpectedContentSHA256 string `json:"expectedContentSHA256,omitempty"`
}

const RepositoryFileReadInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"path":{"type":"string","minLength":1,"maxLength":4096},"commit":{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"},"offset":{"type":"integer","minimum":0,"maximum":8388608},"maxBytes":{"type":"integer","minimum":1,"maximum":65536},"expectedContentSHA256":{"type":"string","pattern":"^[0-9a-f]{64}$"}},"required":["aiSessionId","repositoryId","path"],"additionalProperties":false}`
const RepositoryFileReadOutputSchema = `{"type":"object","properties":{"projectId":{"type":"string"},"repositoryId":{"type":"string"},"workspaceId":{"type":"string"},"sourceKind":{"type":"string","enum":["REGISTERED_WORKTREE","COMMIT"]},"path":{"type":"string"},"headCommit":{"type":"string"},"contentSHA256":{"type":"string"},"sizeBytes":{"type":"integer"},"offset":{"type":"integer"},"nextOffset":{"type":"integer"},"startLine":{"type":"integer"},"endLine":{"type":"integer"},"truncated":{"type":"boolean"},"encoding":{"type":"string","enum":["UTF8","BASE64"]},"content":{"type":"string"}},"required":["projectId","repositoryId","sourceKind","path","headCommit","contentSHA256","sizeBytes","offset","nextOffset","startLine","endLine","truncated","encoding","content"],"additionalProperties":false}`
const RepositoryFileReadDescription = "Read one exact repository-relative file under the existing Session/Project/Repository read boundary, without creating a Review, Run, workspace or file grant. Use repository_search to locate necessary files first. Default source is the registered source worktree, not a managed Task workspace; sourceKind and actual headCommit identify what was read. Optional commit is a full exact commit hash. Pages are byte-bounded (up to 64 KiB) with full contentSHA256, size, line/range and nextOffset; send expectedContentSHA256 on every continuation page to reject version mixing. Full-file observation is capped at 8 MiB; larger files or failed/changed reads are errors, not empty success. UTF8 content may become BASE64 for binary or split code points. This is not a cross-file atomic snapshot or evidence that a managed workspace has the same HEAD. Active Upper Review restrictions still apply; use its grant-bound read tools when required. Reads never authorize implementation or require semantic re-review."

func RepositoryFileReadCore(ctx context.Context, service BridgeService, in RepositoryFileReadInput) (repository.FileReadResult, error) {
	reader, ok := service.(interface {
		ReadRepositoryFile(context.Context, string, string, repository.FileReadInput) (repository.FileReadResult, error)
	})
	if !ok {
		return repository.FileReadResult{}, errors.New("repository file read unavailable")
	}
	out, err := reader.ReadRepositoryFile(ctx, in.AISessionID, in.RepositoryID, repository.FileReadInput{Path: in.Path, Commit: in.Commit, Offset: in.Offset, MaxBytes: in.MaxBytes, ExpectedContentSHA256: in.ExpectedContentSHA256})
	if err != nil {
		return out, publicToolError(err)
	}
	return out, nil
}
func (s *StaticService) ReadRepositoryFile(ctx context.Context, sessionID, repositoryID string, in repository.FileReadInput) (repository.FileReadResult, error) {
	if s.control == nil {
		return repository.FileReadResult{}, errors.New("control API unavailable")
	}
	return s.control.ReadRepositoryFileForSession(ctx, sessionID, repositoryID, in)
}
func registerRepositoryFileReadTool(server *sdkmcp.Server, service BridgeService) {
	addExternalTool(server, "repository_file_read", "Read repository file", RepositoryFileReadDescription, RepositoryFileReadInputSchema, RepositoryFileReadOutputSchema, toolAnnotations("Read repository file", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in RepositoryFileReadInput) (*sdkmcp.CallToolResult, repository.FileReadResult, error) {
		out, err := RepositoryFileReadCore(ctx, service, in)
		return nil, out, err
	})
}
