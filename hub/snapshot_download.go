package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

type ModelInfo struct {
	Sha        string         `json:"sha"`
	Files      []string       `json:"files"`
	Siblings   []ModelSibling `json:"siblings"`
}

type ModelSibling struct {
	RFileName string `json:"rfilename"`
}


func snapshotDownload(client *Client, params *DownloadParams) (string, error) {
	if params.FileName != "" {
		return fileDownload(client, params)
	}

	// check connectivity
	if err := checkConnectivity(params.LocalFilesOnly); err != nil {
		cachedSnapshot, err := findCachedSnapshot(client.CacheDir, params)
		if err != nil {
			return "", fmt.Errorf("cannot find snapshot in cache and downloads are disabled: %w", err)
		}
		return cachedSnapshot, nil
	}

	// get repository info from API
	modelInfo, err := getModelInfo(client, params.Repo)
	if err != nil {
		return "", fmt.Errorf("failed to get repository info: %w", err)
	}

	// setup storage folder
	storageFolder := filepath.Join(
		client.CacheDir,
		repoFolderName(params.Repo.Id, params.Repo.Type),
	)
	snapshotFolder := filepath.Join(storageFolder, "snapshots", modelInfo.Sha)

	// cache commit hash for revision
	if params.Revision != modelInfo.Sha {
		refPath := filepath.Join(storageFolder, "refs", params.Revision)
		os.MkdirAll(filepath.Dir(refPath), 0755)
		if err := os.WriteFile(refPath, []byte(modelInfo.Sha), 0644); err != nil {
			return "", fmt.Errorf("failed to cache revision: %w", err)
		}
	}


	// filter files based on patterns before downloading
	var filesToDownload []string
	for _, sibling := range modelInfo.Siblings {
		filesToDownload = append(filesToDownload, sibling.RFileName)
	}
	filesToDownload = filterFilesByPattern(filesToDownload, params.AllowPatterns, params.IgnorePatterns)

	pd := newParallelDownloader(client, len(filesToDownload), params.Repo.Id)


	// start download
    for _, filename := range filesToDownload {
        fileParams := &DownloadParams{
            Repo:           params.Repo,
            FileName:       filename,
            Revision:       modelInfo.Sha,
            ForceDownload:  params.ForceDownload,
            LocalFilesOnly: params.LocalFilesOnly,
        }
        pd.downloadFile(client, fileParams)
    }

    // wait for all downloads
    pd.Wait()


    // Check for errors
    for err := range pd.errors {
        if err != nil {
            return "", err
        }
    }

    return snapshotFolder, nil
}

func getModelInfo(client *Client, repo *Repo) (*ModelInfo, error) {
	url := fmt.Sprintf("%s/api/models/%s", client.Endpoint, repo.Id)
	if repo.Revision != "" && repo.Revision != "main" {
		url = fmt.Sprintf("%s/resolve/%s", url, repo.Revision)
	}

	fmt.Println("Getting model info from:", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", client.UserAgent)
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", 
			resp.StatusCode, resp.Status)
	}

	// parse response
	var info ModelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to parse model info: %w", err)
	}

	if info.Sha == "" {
		return nil, fmt.Errorf("invalid API response: missing commit hash")
	}

	return &info, nil
}


func findCachedSnapshot(cacheDir string, params *DownloadParams) (string, error) {
	storageFolder := filepath.Join(cacheDir, repoFolderName(params.Repo.Id, params.Repo.Type))

	// try using the revision as commit hash first
	if isCommitHash(params.Revision) {
		snapshotPath := filepath.Join(storageFolder, "snapshots", params.Revision)
		if _, err := os.Stat(snapshotPath); err == nil {
			return snapshotPath, nil
		}
	}

	// try to resolve revision from refs
	refPath := filepath.Join(storageFolder, "refs", params.Revision)
	commitBytes, err := os.ReadFile(refPath)
	if err != nil {
		return "", fmt.Errorf("revision not found in cache: %w", err)
	}

	commitHash := string(commitBytes)
	snapshotPath := filepath.Join(storageFolder, "snapshots", commitHash)
	if _, err := os.Stat(snapshotPath); err == nil {
		return snapshotPath, nil
	}

	return "", fmt.Errorf("snapshot not found in cache")
}


func isCommitHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, char := range s {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
