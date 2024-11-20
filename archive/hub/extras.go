package hub

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/cozy-creator/hf-hub/hub/utils"
)

func isOfflineError(err error) bool {
	if err == nil {
		return false
	}

	var netOpErr *net.OpError
	var dnsErr *net.DNSError
	var syscallErr *os.SyscallError

	// Check if the error is a network operation error
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	// Check if the error is a network operation error
	if errors.As(err, &netOpErr) {
		// Typically, these errors occur due to offline or unreachable network
		if netOpErr.Op == "dial" || netOpErr.Op == "read" || netOpErr.Op == "write" {
			return true
		}
	}

	// Check if the error is a DNS error
	if errors.As(err, &dnsErr) {
		return true
	}

	// Check if the error is a syscall error
	if errors.As(err, &syscallErr) {
		if syscallErr.Err == os.ErrNotExist || syscallErr.Err == os.ErrPermission {
			return false
		}
		return true
	}

	return false
}

func isSymlinkSupported(cacheDir string) (bool, error) {
	if cacheDir == "" {
		return false, nil
	}

	if symlinkSupported != nil {
		if _, ok := symlinkSupported[cacheDir]; ok {
			return true, nil
		}
	}

	fullSrcPath := filepath.Join(cacheDir, "src_test")
	file, err := os.Create(fullSrcPath)
	if err != nil {
		return false, err
	}
	defer func() {
		file.Close()
		os.Remove(fullSrcPath)
	}()

	fullDstPath := filepath.Join(cacheDir, "dst_test")
	err = utils.CreateSymlink(fullSrcPath, fullDstPath)
	if err != nil {
		return false, err
	}
	defer os.Remove(fullDstPath)

	if symlinkSupported == nil {
		symlinkSupported = make(map[string]bool)
	}

	symlinkSupported[cacheDir] = true
	return true, nil
}

func commonPath(path1, path2 string) string {
	dir1 := strings.Split(filepath.Clean(path1), string(filepath.Separator))
	dir2 := strings.Split(filepath.Clean(path2), string(filepath.Separator))

	var commonParts []string
	for i := 0; i < len(dir1) && i < len(dir2); i++ {
		if dir1[i] == dir2[i] {
			commonParts = append(commonParts, dir1[i])
		} else {
			break
		}
	}

	return filepath.Join(commonParts...)
}

func createSymlink(src string, dst string, newBlob bool) error {
	relativeSrc, err := filepath.Rel(filepath.Dir(dst), src)
	if err != nil {
		relativeSrc = ""
		return err
	}

	commonPath := commonPath(src, dst)
	supportSymlinks, err := isSymlinkSupported(commonPath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			supportSymlinks, err = isSymlinkSupported(src)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if supportSymlinks {
		srcRelOrAbs := relativeSrc

		err := utils.CreateSymlink(srcRelOrAbs, dst)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil
			} else {
				if newBlob {
					err := os.Rename(src, dst)
					if err != nil {
						mustCopy(src, dst)
					}
				} else {
					mustCopy(src, dst)
				}
			}
		}
	}

	return nil
}

func normalizeETag(etag string) string {
	etag = strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	etag = strings.Trim(etag, "\"")

	return etag
}

func mustCopy(src string, dst string) {
	srcFile, err := os.Open(src)
	if err != nil {
		panic(err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		panic(err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		panic(err)
	}
}

func expandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	if !strings.HasPrefix(path, "~") {
		return filepath.Clean(path), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	if path == "~" {
		return homeDir, nil
	}

	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(homeDir, path[2:])
	} else {
		// Handle cases like ~user/path
		return "", fmt.Errorf("expanding paths with ~username is not supported")
	}

	return filepath.Clean(path), nil
}
