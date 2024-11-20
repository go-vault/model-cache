package hub

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"strconv"
	"io"
	"encoding/json"
)


type LFSPointer struct {
	Sha256 string
	Size   int
}


func GetToken() string {
	// check environment variable
	token := os.Getenv("HF_TOKEN")
	if token != "" {
		return token
	}

	// check token file (similar to hf_hub python)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	tokenPath := filepath.Join(homeDir, ".cache", "huggingface", "token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return ""
	}

	// clean token i.e. remove any trailing newlines and whitespace
	token = strings.TrimSpace(string(tokenBytes))
	return token
}

func repoFolderName(repoID string, repoType string) string {
	// converts "username/repo" to "models--username--repo" (for models. same goes for datasets and spaces)
	repoParts := strings.Split(repoID, "/")
	parts := append([]string{repoType + "s"}, repoParts...)
	return strings.Join(parts, "--")
}

func getFileMetadata(client *Client, repoId string, filename string, headers *http.Header) (*FileMetadata, error) {
	url := fmt.Sprintf("%s/%s/resolve/%s/%s", 
		client.Endpoint, 
		repoId, 
		DefaultRevision, 
		filename,
	)

	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return nil, err
	}

	if headers != nil {
		req.Header = *headers
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Metadata for regular files
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	commitHash := resp.Header.Get("X-Repo-Commit")
	size, _ := strconv.Atoi(resp.Header.Get("Content-Length"))

	// Handle LFS pointer fallback
	if etag == "" || commitHash == "" {
		pointerData, err := fetchLFSPointer(client.Endpoint, repoId, filename)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch LFS pointer: %w", err)
		}
		etag = pointerData.Sha256
		size = pointerData.Size

		commitHash, err = fetchCommitHash(client.Endpoint, repoId)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch commit hash: %w", err)
		}
	}

	// Build metadata object
	metadata := &FileMetadata{
		CommitHash: commitHash,
		ETag:       etag,
		Location:   resp.Header.Get("Location"),
		Size:       size,
	}

	// use request URL if no redirect location
	if metadata.Location == "" {
		metadata.Location = url
	}

	return metadata, nil
}


func fetchCommitHash(endpoint, repoId string) (string, error) {
	url := fmt.Sprintf("%s/api/models/%s", endpoint, repoId)

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch commit hash: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		CommitHash string `json:"sha"`
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return "", fmt.Errorf("failed to decode commit hash: %w", err)
	}

	return result.CommitHash, nil
}

func fetchLFSPointer(endpoint, repoId, filename string) (*LFSPointer, error) {
	rawURL := fmt.Sprintf("%s/%s/raw/%s/%s", endpoint, repoId, DefaultRevision, filename)
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch LFS pointer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// parse LFS pointer
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read LFS pointer: %w", err)
	}

	// extract sha256 and size from pointer
	lines := strings.Split(string(body), "\n")
	var sha256 string
	var size int
	for _, line := range lines {
		if strings.HasPrefix(line, "oid sha256:") {
			sha256 = strings.TrimPrefix(line, "oid sha256:")
		} else if strings.HasPrefix(line, "size ") {
			size, _ = strconv.Atoi(strings.TrimPrefix(line, "size "))
		}
	}

	if sha256 == "" || size == 0 {
		return nil, fmt.Errorf("invalid LFS pointer")
	}

	return &LFSPointer{
		Sha256: sha256,
		Size:   size,
	}, nil
}


func expandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get user home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}

	// convert to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	cleanPath := filepath.Clean(absPath)

	return cleanPath, nil
}


func createSymlink(srcPath, dstPath string) error {

	srcAbs, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of source: %w", err)
	}

	dstAbs, err := filepath.Abs(dstPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of destination: %w", err)
	}

	// create relative path from symlink -> target
	relPath, err := filepath.Rel(filepath.Dir(dstAbs), srcAbs)
	if err != nil {
		return fmt.Errorf("failed to determine relative path: %w", err)
	}

	// remove existing destination if exists
	if _, err := os.Lstat(dstAbs); err == nil {
		os.Remove(dstAbs)
	}

	// ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// create symlink
	if err := os.Symlink(relPath, dstAbs); err != nil {
		// if symlink creation fails, fall back to copying the file
		srcFile, err := os.Open(srcAbs)
		if err != nil {
			return fmt.Errorf("failed to open source file: %w", err)
		}
		defer srcFile.Close()

		dstFile, err := os.Create(dstAbs)
		if err != nil {
			return fmt.Errorf("failed to create destination file: %w", err)
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("failed to copy source file to destination: %w", err)
		}
	}

	return nil
}




func IsOfflineMode() bool {
	return os.Getenv("HF_HUB_OFFLINE") == "1"
}

func checkConnectivity(localFilesOnly bool) error {
	if IsOfflineMode() {
		return fmt.Errorf("cannot download files as offline mode is enabled (HF_HUB_OFFLINE=1)")
	}
	if localFilesOnly {
		return fmt.Errorf("cannot download files as local_files_only is set to true")
	}

	return nil
}

