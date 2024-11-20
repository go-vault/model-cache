package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

func snapshotDownload(client *Client, params *DownloadParams) (string, error) {
	repo := params.Repo
	localFilesOnly := params.LocalFilesOnly

	wg := sync.WaitGroup{}
	var (
		modelInfo *ModelInfo
		err       error
	)

	if !localFilesOnly {
		modelInfo, err = client.getModelInfo(repo)
		if err != nil && !isOfflineError(err) {
			return "", err
		}
	}

	storageFolder := filepath.Join(client.CacheDir, repoFolderName(repo.Id, repo.Type))
	var commitHash string

	// modelInfo == nil means localFilesOnly is set to true or we're offline, so we cannot download the model.
	// instead, we'll try to get the commit hash, and reolve a cached snapshot of the repo.
	// if we can't find it, we'll return an error.
	if modelInfo == nil {
		if regexp.MustCompile(CommitHashPattern).MatchString(repo.Revision) {
			commitHash = repo.Revision
		} else {
			refPath := filepath.Join(storageFolder, "refs", repo.Revision)
			_, err := os.Stat(refPath)
			if err == nil {
				content, err := os.ReadFile(refPath)
				if err != nil {
					return "", err
				}
				commitHash = string(content)
			} else {
				return "", err
			}
		}

		if commitHash != "" {
			snapshotFolder := filepath.Join(storageFolder, "snapshots", commitHash)
			_, err := os.Stat(snapshotFolder)
			if err == nil {
				return snapshotFolder, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}

	// we're using localFilesOnly and we cannot find a cached snapshot folder for the specified revision. so we return an error.
	if localFilesOnly {
		return "", fmt.Errorf(
			"cannot find an appropriate cached snapshot folder for the specified revision on the local disk and outgoing traffic has been disabled. To enable repo look-ups and downloads online, set localFilesOnly to false",
		)
	}

	if modelInfo.Sha == "" {
		return "", fmt.Errorf("no sha found for this model")
	}

	if modelInfo.Siblings == nil {
		return "", fmt.Errorf("no siblings found for this model")
	}

	commitHash = modelInfo.Sha
	snapshotFolder := filepath.Join(storageFolder, "snapshots", commitHash)

	if repo.Revision != commitHash {
		refPath := filepath.Join(storageFolder, "refs", repo.Revision)
		_, err = os.Stat(refPath)
		if err == nil {
			err := os.WriteFile(refPath, []byte(commitHash), os.ModePerm)
			if err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}

	download := func(fileName string) {
		wg.Add(1)
		defer wg.Done()
		fileParams := &DownloadParams{
			Repo:           repo,
			FileName:       fileName,
			Revision:       params.Revision,
			ForceDownload:  params.ForceDownload,
			LocalFilesOnly: params.LocalFilesOnly,
		}

		fileDownload(client, fileParams)
	}

	for _, sibling := range modelInfo.Siblings {
		go download(sibling.RFileName)
	}

	wg.Wait()
	return snapshotFolder, nil
}

func (c *Client) getModelInfo(repo *Repo) (*ModelInfo, error) {
	headers := &http.Header{}
	headers.Set("User-Agent", DefaultUserAgent)

	if repo.Type != ModelRepoType {
		return nil, fmt.Errorf("invalid repo type: %s", repo.Type)
	}

	var (
		modelInfoUrl string
		err          error
	)

	if repo.Revision != "" {
		modelInfoUrl, err = formatUrl(
			hfModelRevisionInfoTemplate,
			map[string]string{
				"Endpoint": c.Endpoint,
				"RepoId":   repo.Id,
				"Revision": repo.Revision,
			},
		)
	} else {
		modelInfoUrl, err = formatUrl(
			hfModelRevisionInfoTemplate,
			map[string]string{
				"Endpoint": c.Endpoint,
				"RepoId":   repo.Id,
			},
		)
	}

	if err != nil {
		return nil, err
	}

	response, err := requestWrapper("GET", modelInfoUrl, false, true, headers)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var data = &ModelInfo{}
	err = json.NewDecoder(response.Body).Decode(&data)
	if err != nil {
		return nil, err
	}

	return data, nil
}
