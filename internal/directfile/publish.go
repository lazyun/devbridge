package directfile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func publishCreate(root *os.Root, target string, content []byte) error {
	temp, err := writeTemp(root, target, content, 0o644)
	if err != nil {
		return err
	}
	defer removeFile(root, temp)
	if err = root.Link(temp, target); err != nil {
		return fileError(err)
	}
	return nil
}

func publishReplace(root *os.Root, target string, content []byte) error {
	return publishReplaceWith(root, target, content, root.Rename)
}

func publishReplaceWith(root *os.Root, target string, content []byte, rename func(string, string) error) error {
	info, err := root.Lstat(target)
	if err != nil {
		return fileError(err)
	}
	if !info.Mode().IsRegular() {
		return Failure(InvalidTarget, "path is not a regular file")
	}
	temp, err := writeTemp(root, target, content, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer removeFile(root, temp)
	if err = rename(temp, target); err != nil {
		return fileError(err)
	}
	return nil
}

func writeTemp(root *os.Root, target string, content []byte, mode os.FileMode) (string, error) {
	dir := filepath.Dir(target)
	for attempt := 0; attempt < 32; attempt++ {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", Failure(OperationFailed, "cannot generate temporary filename")
		}
		name := filepath.Join(dir, fmt.Sprintf(".directfile-%x.tmp", nonce))
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fileError(err)
		}
		n, writeErr := f.Write(content)
		if writeErr == nil && n != len(content) {
			writeErr = io.ErrShortWrite
		}
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			removeFile(root, name)
			if writeErr != nil {
				return "", fileError(writeErr)
			}
			return "", fileError(closeErr)
		}
		return name, nil
	}
	return "", Failure(OperationFailed, "cannot allocate temporary filename")
}
