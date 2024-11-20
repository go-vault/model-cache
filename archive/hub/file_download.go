package hub

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/cozy-creator/hf-hub/hub/utils"
	"github.com/gofrs/flock"
	"github.com/schollz/progressbar/v3"
)

func downloadFileStream(url string, incompleteFile *os.File, resumeSize int64, headers *http.Header, expectedSize int64, displayedFilename string, nbRetries int) error {
	currentHeaders := headers.Clone()
	if resumeSize > 0 {
		currentHeaders.Set("Range", fmt.Sprintf("bytes=%d-", resumeSize))
	}

	r, err := requestWrapper("GET", url, true, true, &currentHeaders)
	if err != nil {
		if nbRetries <= 0 {
			return fmt.Errorf("error while downloading from %s: %s\nMax retries exceeded", url, err)
		}

		if errors.Is(err, http.ErrHandlerTimeout) {
			log.Printf("error while downloading from %s: %s\nTrying to resume download...\n", url, err)
			time.Sleep(1 * time.Second)
			return downloadFileStream(url, incompleteFile, resumeSize, headers, expectedSize, displayedFilename, nbRetries-1)
		}

		return err
	}
	defer r.Body.Close()

	// move to the position where we left off
	_, err = incompleteFile.Seek(resumeSize, io.SeekStart)
	if err != nil {
		return err
	}

	contentLength, err := strconv.Atoi(r.Header.Get("Content-Length"))
	if err != nil {
		return err
	}

	// NOTE: 'totalBytes' is the totalBytes number of bytes to download, not the number of bytes in the file.
	//       If the file is compressed, the number of bytes in the saved file will be higher than 'totalBytes'.
	totalBytes := resumeSize + int64(contentLength)
	// if totalBytes != expectedSize {
	// 	return fmt.Errorf("expected size %d does not match actual size %d", expectedSize, totalBytes)
	// }

	if displayedFilename == "" {
		displayedFilename = url
		contentDisposition := r.Header.Get("Content-Disposition")
		if contentDisposition != "" {
			headerFilenamePattern := regexp.MustCompile(HeaderFilenamePattern)
			match := headerFilenamePattern.FindStringSubmatch(contentDisposition)
			if len(match) > 0 {
				// Means file is on CDN
				displayedFilename = match[1]
			}
		}
	}

	if len(displayedFilename) > 40 {
		displayedFilename = fmt.Sprintf("(…)%s", displayedFilename[len(displayedFilename)-40:])
	}

	consistencyErrorMessage := fmt.Sprintf("Consistency check failed: file should be of size %d but has size %d (%s). We are sorry for the inconvenience. Please retry with `force_download=True`. If the issue persists, please let us know by opening an issue on https://github.com/huggingface/huggingface_hub.", expectedSize, 0 /*/*incompleteFile.Tell()*/, displayedFilename)

	progressbar := progressbar.DefaultBytes(totalBytes)
	progressbar.Set64(resumeSize)

	newResumeSize := resumeSize
	for {
		if nbRetries <= 0 {
			return fmt.Errorf("error while downloading from %s: %s\nMax retries exceeded", url, err)
		}

		buf := make([]byte, DownloadChunkSize)
		n, err := r.Body.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		if n > 0 {
			// Write the actual bytes read to the file
			if _, writeErr := incompleteFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("error writing to file: %v", writeErr)
			}

			progressbar.Add64(int64(n))
			newResumeSize += int64(n)

			// Some data has been downloaded from the server so we reset the number of retries.
			nbRetries = 5
		}
	}

	if expectedSize != 0 && newResumeSize != expectedSize {
		return fmt.Errorf(consistencyErrorMessage, newResumeSize)
	}
	return nil
}

func formatUrl(urlTemplate string, params map[string]string) (string, error) {
	tmpl, err := template.New("url").Parse(urlTemplate)
	if err != nil {
		return "", err
	}

	var urlBytes bytes.Buffer
	if err = tmpl.Execute(&urlBytes, params); err != nil {
		return "", err
	}

	return urlBytes.String(), nil
}

func requestWrapper(
	method,
	rawUrl string,
	allowRedirects,
	followRelativeRedirects bool,
	headers *http.Header,
) (*http.Response, error) {
	if followRelativeRedirects {
		response, err := requestWrapper(method, rawUrl, allowRedirects, false, headers)
		if err != nil {
			return nil, err
		}

		if response.StatusCode >= 300 && response.StatusCode <= 399 {
			parsedTarget, err := url.Parse(response.Header.Get("Location"))
			if err != nil {
				return nil, err
			}
			if parsedTarget.Host == "" {
				// This means it is a relative 'location' headers, as allowed by RFC 7231.
				// (e.g. '/path/to/resource' instead of 'http://domain.tld/path/to/resource')
				// We want to follow this relative redirect !
				//
				// Highly inspired by `resolve_redirects` from requests library.
				// See https://github.com/psf/requests/blob/main/requests/sessions.py#L159
				nextUrl := url.URL{
					Scheme:   parsedTarget.Scheme,
					Host:     parsedTarget.Host,
					Path:     parsedTarget.Path,
					RawQuery: parsedTarget.RawQuery,
				}

				return requestWrapper(method, nextUrl.String(), allowRedirects, true, headers)
			}
		}
	}

	var (
		response *http.Response
		err      error = nil
	)

	request, err := http.NewRequest(method, rawUrl, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{}
	if !allowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	if headers != nil {
		request.Header = *headers
	}

	response, err = client.Do(request)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func downloadToTmpAndMove(incompletePath, destinationPath, downloadUrl string, headers *http.Header, expectedSize int, filename string, forceDownload bool) error {
	if _, err := os.Stat(destinationPath); err == nil && !forceDownload {
		// Do nothing if already exists (except if force_download=True)
		return nil
	}

	_, err := os.Stat(incompletePath)
	if err == nil {
		// By default, we will try to resume the download if possible.
		// However, if the user has set `force_download=True`, we should not resume the download => delete the incomplete file.
		message := fmt.Sprintf("Removing incomplete file '%s'", incompletePath)
		if forceDownload {
			message += " (force_download=True)"
		}

		log.Println(message)
		os.Remove(incompletePath)
	}

	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if forceDownload {
			message := fmt.Sprintf("Removing incomplete file '%s'", incompletePath)
			if forceDownload {
				message += " (force_download=True)"
			}

			log.Println(message)
			os.Remove(incompletePath)
		}
	}

	incompleteFile, err := os.OpenFile(incompletePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	defer incompleteFile.Close()

	resumeSize, err := incompleteFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	message := fmt.Sprintf("Downloading '%s' to '%s'", filename, incompletePath)
	if resumeSize > 0 && expectedSize != 0 {
		message += fmt.Sprintf(" (resume from %d/%d)", resumeSize, expectedSize)
	}
	log.Println(message)

	if expectedSize != 0 {
		// Check disk space in both tmp and destination path
		incompletePathDir := filepath.Dir(incompletePath)
		size, err := utils.GetAvailableDiskSpace(incompletePathDir)
		if err != nil {
			return err
		}
		if expectedSize > 0 && size < uint64(expectedSize) {
			return fmt.Errorf("not enough free disk space to download the file. The expected file size is: %d MB. The target location %s only has %d MB free disk space", expectedSize/1000000, incompletePathDir, size/1000000)
		}

		destinationPathDir := filepath.Dir(destinationPath)
		size, err = utils.GetAvailableDiskSpace(destinationPathDir)
		if err != nil {
			return err
		}

		if expectedSize > 0 && size < uint64(expectedSize) {
			return fmt.Errorf("not enough free disk space to download the file. The expected file size is: %d MB. The target location %s only has %d MB free disk space", expectedSize/1000000, destinationPathDir, size/10666)
		}
	}

	err = downloadFileStream(downloadUrl, incompleteFile, resumeSize, headers, int64(expectedSize), filename, DefaultRetries)

	if err != nil {
		return err
	}

	os.Rename(incompletePath, destinationPath)
	return nil
}

func repoFolderName(repoId string, repoType string) string {
	repoParts := strings.Split(repoId, "/")
	repo := append([]string{repoType + "s"}, repoParts...)

	return strings.Join(repo, "--")
}

func cacheCommitHashForSpecificRevision(storageFolder string, revision string, commitHash string) error {
	if revision != commitHash {
		refPath := filepath.Join(storageFolder, "refs", revision)
		err := os.MkdirAll(filepath.Dir(refPath), os.ModePerm)
		if err != nil {
			panic(err)
		}

		_, err = os.Stat(refPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				os.WriteFile(refPath, []byte(commitHash), os.ModePerm)
			}

			return err
		}

		file, err := os.ReadFile(refPath)
		if err != nil {
			return err
		}
		if string(file) != commitHash {
			os.WriteFile(refPath, []byte(commitHash), os.ModePerm)
		}

		return nil
	}
	return nil

}

func fileDownload(client *Client, params *DownloadParams) (string, error) {
	repoId := params.Repo.Id
	fileName := params.FileName
	repoType := params.Repo.Type
	forceDownload := params.ForceDownload

	if params.SubFolder != "" {
		fileName = fmt.Sprintf("%s/%s", params.SubFolder, fileName)
	}

	if repoType != SpaceRepoType && repoType != DatasetRepoType && repoType != ModelRepoType {
		return "", fmt.Errorf("invalid repo type: %s", repoType)
	}

	repoFolderName := repoFolderName(repoId, repoType)
	storageFolder := filepath.Join(client.CacheDir, repoFolderName)
	err := os.MkdirAll(storageFolder, os.ModePerm)
	if err != nil {
		return "", err
	}

	snapshotPath := filepath.Join(storageFolder, "snapshots")

	revision := params.Revision
	if regexp.MustCompile(CommitHashPattern).MatchString(revision) {
		pointerPath := filepath.Join(snapshotPath, revision, fileName)
		_, err := os.Stat(pointerPath)
		if err == nil && !forceDownload {
			return pointerPath, nil
		} else {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}

	headers := &http.Header{}
	headers.Set("User-Aget", client.UserAgent)

	urlParams := map[string]string{
		"Endpoint": client.Endpoint,
		"RepoId":   repoId,
		"Revision": revision,
		"Filename": fileName,
	}
	hfResolveUrl, err := formatUrl(hfResolveUrlTemplate, urlParams)
	if err != nil {
		return "", err
	}

	fileMetadata, err := getFileMetadata(hfResolveUrl, headers)
	if err != nil {
		if strings.Contains(err.Error(), "EntryNotFound") {
			if fileMetadata != nil && fileMetadata.CommitHash != "" {
				noExistPath := filepath.Join(storageFolder, ".no_exist", fileMetadata.CommitHash)
				os.MkdirAll(noExistPath, os.ModePerm)

				noExistFilePath := filepath.Join(noExistPath, fileName)
				os.Create(noExistFilePath)
				cacheCommitHashForSpecificRevision(storageFolder, revision, fileMetadata.CommitHash)
			}
		}

		return "", err
	}

	if fileMetadata == nil {
		return "", fmt.Errorf("error while retrieving file metadata: %s", err)
	}

	if fileMetadata.CommitHash == "" {
		return "", fmt.Errorf("no commit hash found for this file. It is likely that the file is not yet available on HF Hub")
	}

	if fileMetadata.ETag == "" {
		return "", fmt.Errorf("no ETag found for this file. It is likely that the file is not yet available on HF Hub")
	}

	if fileMetadata.Size == 0 {
		return "", fmt.Errorf("no size found for this file. It is likely that the file is not yet available on HF Hub")
	}

	if fileMetadata.Location != hfResolveUrl {
		parsedLocation, err := url.Parse(fileMetadata.Location)
		if err != nil {
			return "", err
		}

		parsedHfUrl, err := url.Parse(hfResolveUrl)
		if err != nil {
			return "", err
		}

		if parsedLocation.Host != parsedHfUrl.Host {
			// Remove authorization header when downloading a LFS blob
			headers.Del("authorization")
		}
	}

	if !(params.LocalFilesOnly || fileMetadata.ETag != "") {
		return "", fmt.Errorf("error while retrieving file metadata: %s", err)
	}

	var commitHash string
	if fileMetadata.CommitHash != "" {
		if regexp.MustCompile(CommitHashPattern).MatchString(fileMetadata.CommitHash) {
			commitHash = fileMetadata.CommitHash
		} else {
			refPath := filepath.Join(storageFolder, "refs", revision)
			_, err := os.Stat(refPath)
			if err == nil {
				content, err := os.ReadFile(refPath)
				if err != nil {
					return "", err
				}
				commitHash = string(content)
			}

			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}

	if commitHash != "" {
		pointerPath := filepath.Join(snapshotPath, commitHash, fileName)
		_, err := os.Stat(pointerPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		if err == nil && !forceDownload {
			return pointerPath, nil
		}
	}

	blobPath := filepath.Join(storageFolder, "blobs", fileMetadata.ETag)
	pointerPath := filepath.Join(snapshotPath, commitHash, fileName)

	os.MkdirAll(filepath.Dir(blobPath), os.ModePerm)
	os.MkdirAll(filepath.Dir(pointerPath), os.ModePerm)

	cacheCommitHashForSpecificRevision(storageFolder, revision, commitHash)

	if !forceDownload {
		if _, err := os.Stat(pointerPath); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else {
			return pointerPath, nil
		}

		_, err := os.Stat(blobPath)
		if err == nil {
			createSymlink(blobPath, pointerPath, false)
			return pointerPath, nil
		} else {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}

	incompletePath := fmt.Sprintf("%s.incomplete", blobPath)
	destinationPath := blobPath
	// destinationPath := filepath.Join(cacheDir, repoFolderName, fileName)

	lockFolder := filepath.Join(storageFolder, ".locks")
	err = os.MkdirAll(lockFolder, os.ModePerm)
	if err != nil {
		return "", err
	}

	lockPath := filepath.Join(lockFolder, fmt.Sprintf("%s.lock", fileMetadata.ETag))
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return "", err
	}

	if !locked {
		return "", fmt.Errorf("failed to acquire lock for %s", fileMetadata.ETag)
	}

	defer lock.Unlock()

	err = downloadToTmpAndMove(incompletePath, destinationPath, hfResolveUrl, headers, fileMetadata.Size, fileName, forceDownload)
	if err != nil {
		return "", err
	}

	createSymlink(destinationPath, pointerPath, true)
	return pointerPath, nil
}

func getFileMetadata(url string, headers *http.Header) (*FileMetadata, error) {
	headers.Set("Accept-Encoding", "identity")
	response, err := requestWrapper("HEAD", url, false, true, headers)

	if response.StatusCode >= 400 {
		m := &FileMetadata{
			CommitHash: response.Header.Get("X-Repo-Commit"),
		}

		errorCode := response.Header.Get("X-Error-Code")
		errorTemplate := "error while retrieving file metadata from %s due to %s"
		return m, fmt.Errorf(errorTemplate, url, errorCode)
	}

	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	commitHash := response.Header.Get("X-Repo-Commit")

	etag := response.Header.Get("X-Linked-Etag")
	if etag == "" {
		etag = response.Header.Get("ETag")
	}

	sizeStr := response.Header.Get("X-Linked-Size")
	if sizeStr == "" {
		sizeStr = response.Header.Get("Content-Length")
	}

	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		return nil, err
	}

	location := response.Header.Get("Location")
	if location == "" {
		location = response.Request.URL.String()
	}

	return &FileMetadata{
		Size:       size,
		Location:   location,
		CommitHash: commitHash,
		ETag:       normalizeETag(etag),
	}, nil
}
