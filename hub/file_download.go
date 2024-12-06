package hub

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gofrs/flock"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)


const (
	DownloadChunkSize = 1024 * 1024 // 1MB
	DefaultRetries    = 5
)


func (client *Client) Download(params *DownloadParams) (string, error) {
	// set defaults if not provided
	if params.Repo.Type == "" {
		params.Repo.Type = ModelRepoType
	}
	if params.Revision == "" {
		params.Revision = DefaultRevision
	}
	if params.Repo.Revision == "" {
		params.Repo.Revision = params.Revision
	}

	// if no filename is specified, use snapshot downloader
	if params.FileName == "" {
		return snapshotDownload(client, params)
	}

	// otherwise, download the file
	return fileDownload(client, params)
}

func fileDownload(client *Client, params *DownloadParams) (string, error) {
	repoId := params.Repo.Id
	fileName := params.FileName
	repoType := params.Repo.Type


	// check if we can download
	if err := checkConnectivity(params.LocalFilesOnly); err != nil {
		cachedPath, err := findInCache(client.CacheDir, repoId, repoType, fileName, params.Revision)
		if err != nil {
			return "", fmt.Errorf("file not found in cache and downloads are disabled: %w", err)
		}
		return cachedPath, nil
	}

	// handle subfolder in filename
	if params.SubFolder != "" {
		fileName = filepath.Join(params.SubFolder, fileName)
	}

	if repoType != ModelRepoType && repoType != SpaceRepoType && repoType != DatasetRepoType {
		return "", fmt.Errorf("unsupported repo type: %s", repoType)
	}

	// setup storage folder
	storageFolder := filepath.Join(client.CacheDir, repoFolderName(repoId, repoType))
	if err := os.MkdirAll(storageFolder, 0755); err != nil {
		return "", err
	}

	// check for commmmit hash revision
	if regexp.MustCompile("^[0-9a-f]{40}$").MatchString(params.Revision) {
		pointerPath := filepath.Join(storageFolder, "snapshots", params.Revision, fileName)
		if _, err := os.Stat(pointerPath); err == nil && !params.ForceDownload {
			return pointerPath, nil
		}
	}

	// prepare headers for request
	headers := &http.Header{}
	headers.Set("User-Agent", client.UserAgent)
	if client.Token != "" {
		headers.Set("Authorization", "Bearer "+client.Token)
	}

	// get file metadata
	fileMetadata, err := getFileMetadata(client, params.Repo.Id, fileName, headers)
	if err != nil {
		return "", fmt.Errorf("failed to get file metadata: %w", err)
	}

	// setup paths
	blobPath := filepath.Join(storageFolder, "blobs", fileMetadata.ETag)
	pointerPath := filepath.Join(storageFolder, "snapshots", fileMetadata.CommitHash, fileName)

	//create directories
	os.MkdirAll(filepath.Dir(blobPath), 0755)
	os.MkdirAll(filepath.Dir(pointerPath), 0755)

	// cache commit hash
	if params.Revision != fileMetadata.CommitHash {
		refPath := filepath.Join(storageFolder, "refs", params.Revision)
		os.MkdirAll(filepath.Dir(refPath), 0755)
		if err := os.WriteFile(refPath, []byte(fileMetadata.CommitHash), 0644); err != nil {
			return "", fmt.Errorf("failed to cache commit hash: %w", err)
		}
	}

	// return early if file exists
	if !params.ForceDownload {
		if _, err := os.Stat(pointerPath); err == nil {
			return pointerPath, nil
		}
		if _, err := os.Stat(blobPath); err == nil {
			if err := createSymlink(blobPath, pointerPath); err != nil {
				return "", err
			}
			return pointerPath, nil
		}
	}

	// lock directory for concurrent downloads
	locksDir := filepath.Join(client.CacheDir, ".locks")
	if err := os.MkdirAll(locksDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create locks directory: %w", err)
	}

	modelLockDir := filepath.Join(locksDir, repoFolderName(repoId, repoType))
	if err := os.MkdirAll(modelLockDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create model locks directory: %w", err)
	}


	lockPath := filepath.Join(modelLockDir, fmt.Sprintf("%s.lock", fileMetadata.ETag))
	fileLock := flock.New(lockPath)

	locked, err := fileLock.TryLock()
	if err != nil {
		return "", fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return "", fmt.Errorf("failed to acquire lock for %s", fileMetadata.ETag)
	}

	defer fileLock.Unlock()

	// download file
	tmpPath := blobPath + ".incomplete"
	if err := downloadFile(client, fileMetadata.Location, tmpPath, headers, fileMetadata.Size, fileName); err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}

	// move temporary file to final destination
	if err := os.Rename(tmpPath, blobPath); err != nil {
		return "", fmt.Errorf("failed to move temporary file to final destination: %w", err)
	}

	// create symlink
	if err := createSymlink(blobPath, pointerPath); err != nil {
		return "", err
	}

	return pointerPath, nil
}


func downloadFile(client *Client, url, destPath string, headers *http.Header, expectedSize int, displayName string) error {
	// try to get existing file for resume
	var resumeSize int64 = 0
	if stat, err := os.Stat(destPath); err == nil {
		resumeSize = stat.Size()
	}

	// use append mode if resuming, else create new
	flag := os.O_CREATE | os.O_WRONLY
	if resumeSize > 0 {
		flag |= os.O_APPEND
	}

	out, err := os.OpenFile(destPath, flag, 0644)
	if err != nil {
		return err
	}

	defer out.Close()

	httpClient := &http.Client{
		Timeout: time.Minute * 30,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	if headers != nil {
		req.Header = *headers
	}

	if resumeSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeSize))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resumeSize > 0 && resp.StatusCode != http.StatusPartialContent {
		// server doesn't support resume, start over
		resumeSize = 0
		out.Seek(0, 0)
		out.Truncate(0)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// progress bar
	description := fmt.Sprintf("Downloading %s", displayName)
	if resumeSize > 0 {
		description = fmt.Sprintf("Resuming download of %s", displayName)
	}

	bar := client.Progress.AddBar(
        int64(expectedSize),
        mpb.PrependDecorators(
            decor.Name(description+": ", decor.WC{W: len(description) + 2, C: decor.DidentRight}),
            decor.Percentage(decor.WCSyncSpace),
        ),
        mpb.AppendDecorators(
            decor.CountersKibiByte("%.2f / %.2f"),
            decor.EwmaETA(decor.ET_STYLE_GO, 60),
            decor.EwmaSpeed(decor.UnitKiB, "%.2f", 60),
        ),
    )

	// set initial progress if resuming
	if resumeSize > 0 {
		bar.SetCurrent(resumeSize)
	}

	reader := bar.ProxyReader(resp.Body)
	defer reader.Close()

	buf := make([]byte, 64*1024) // 64KB buffer

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// write to file
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}

			// bar.Add(n)
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}

	bar.SetTotal(bar.Current(), true)

	return nil
}


func findInCache(cacheDir, repoId, repoType, fileName, revision string) (string, error) {
	storageFolder := filepath.Join(cacheDir, repoFolderName(repoId, repoType))

	// if revision is a commit hash, look for it in snapshots
	if regexp.MustCompile("^[0-9a-f]{40}$").MatchString(revision) {
		path := filepath.Join(storageFolder, "snapshots", revision, fileName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("file not found in cache at revision %s", revision)
	}

	// else, try to resolve the revision from refs
	refPath := filepath.Join(storageFolder, "refs", revision)
	commitHash, err := os.ReadFile(refPath)
	if err != nil {
		return "", fmt.Errorf("revision %s not found in cache", revision)
	}

	path := filepath.Join(storageFolder, "snapshots", string(commitHash), fileName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("file not found in cache")
}

	
	
