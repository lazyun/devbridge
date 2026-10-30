package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LocalBranch is a read-only observation, never a workflow selection.
type LocalBranch struct {
	FullRef            string `json:"fullRef"`
	ShortName          string `json:"shortName"`
	HeadSHA            string `json:"headSha"`
	RepositoryCheckout bool   `json:"repositoryCheckout"`
}

type BranchInventory struct {
	Branches    []LocalBranch `json:"branches"`
	CheckoutRef string        `json:"repositoryCheckoutRef"`
	Detached    bool          `json:"repositoryCheckoutDetached"`
}

// LocalBranches enumerates only direct, commit-valued refs/heads/*, in full-ref
// order. symbolic aliases, remote refs, tags and detached HEAD are not choices.
// Neither command refreshes the index or touches repository files.
func (c *Client) LocalBranches(ctx context.Context, path string) (BranchInventory, error) {
	result := BranchInventory{Branches: []LocalBranch{}}
	head, err := c.readBranchFacts(ctx, path, true, "read checkout ref", "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return result, err
	}
	result.CheckoutRef = strings.TrimSpace(string(head))
	result.Detached = result.CheckoutRef == ""
	out, err := c.readBranchFacts(ctx, path, false, "list local branches", "for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(symref)", "refs/heads/")
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 4 {
			return result, fmt.Errorf("invalid local branch inventory")
		}
		if !strings.HasPrefix(fields[0], "refs/heads/") || fields[2] != "commit" || fields[3] != "" {
			continue
		}
		if !isObjectID(fields[1]) {
			return result, fmt.Errorf("invalid branch head")
		}
		result.Branches = append(result.Branches, LocalBranch{FullRef: fields[0], ShortName: strings.TrimPrefix(fields[0], "refs/heads/"), HeadSHA: fields[1], RepositoryCheckout: fields[0] == result.CheckoutRef})
	}
	return result, nil
}

// Branch facts must belong to the registered path even when Core was launched
// from a shell carrying Git repository/index/config overrides. This command
// environment is local to the new observation operation.
func (c *Client) readBranchFacts(ctx context.Context, path string, allowExitOne bool, operation string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.Dir = path
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	stdout, stderr := &limitedBuffer{limit: c.maxOutput}, &limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, &OutputLimitError{Operation: operation, Limit: c.maxOutput}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if !(allowExitOne && errors.As(err, &exit) && exit.ExitCode() == 1) {
			return nil, &CommandError{Operation: operation, Args: append([]string(nil), args...), Stderr: strings.TrimSpace(stderr.String()), Err: err}
		}
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}
