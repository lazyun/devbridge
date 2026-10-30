package platformcollector

import (
	"context"
	"path/filepath"

	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

// NativeLaunch captures only this provider's measured artifacts. It conveys no
// qualification by itself; the caller must first verify the revision binding.
func (p *Provider) NativeLaunch(ctx context.Context, descriptorID string) (codexlaunch.VerifiedExecutable, error) {
	if _, err := p.ResolveDescriptor(ctx, descriptorID); err != nil {
		return nil, err
	}
	return nativeLaunch{manifest: p.manifest}, nil
}

type nativeLaunch struct{ manifest Manifest }

func (v nativeLaunch) Executable(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(v.manifest.NativePath) || v.manifest.NativeDigest == "" {
		return "", runtimeplatform.ErrIdentityMismatch
	}
	p := Provider{manifest: v.manifest}
	if err := p.revalidateArtifacts(); err != nil {
		return "", err
	}
	if err := nativeFile(v.manifest.NativePath); err != nil {
		return "", err
	}
	return v.manifest.NativePath, ctx.Err()
}
