package hub

import (
	"os"
	"path/filepath"
	"fmt"
)

const (
	ModelRepoType   = "model"
	SpaceRepoType   = "space"
	DatasetRepoType = "dataset"

	DefaultRevision = "main"
	DefaultCacheDir = "~/.cache/huggingface/hub"
)


type Client struct {
	Endpoint        string
	Token           string
	CacheDir        string
	UserAgent       string
}


func (client *Client) WithToken(token string) *Client {
    client.Token = token
    return client
}

func NewClient(endpoint string, token string, cacheDir string) *Client {
	expandedCache, err := expandPath(cacheDir)
	if err != nil {
		panic(err)
	}

	return &Client{
		Endpoint: 	endpoint,
		Token:   	token,
		CacheDir:   expandedCache,
		UserAgent:  "huggingface-go/0.0.1",
	}
}

func DefaultClient() *Client {
	var cacheDir string

	if xdgCache := os.Getenv("XDG_CACHE_HOME"); xdgCache != "" {
		cacheDir = filepath.Join(xdgCache, "huggingface", "hub")
	}

	if cacheDir == "" {
		if hfCache := os.Getenv("HF_HUB_CACHE"); hfCache != "" {
			cacheDir = hfCache
		}
	}

	if cacheDir == "" {
		if hfHome := os.Getenv("HF_HOME"); hfHome != "" {
			cacheDir = filepath.Join(hfHome, "hub")
		}
	}

	if cacheDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("failed to get user home directory: %w", err))
		}
		cacheDir = filepath.Join(homeDir, ".cache", "huggingface", "hub")
	}

	expandedCache, err := expandPath(cacheDir)
	if err != nil {
		panic(fmt.Errorf("failed to expand cache directory: %w", err))
	}

	// create cache directory if it doesn't exist
	if err := os.MkdirAll(expandedCache, 0755); err != nil {
		panic(fmt.Errorf("failed to create cache directory: %w", err))
	}

	endpoint := os.Getenv("HF_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://huggingface.co"
	}

	token := GetToken()

	return &Client{
		Endpoint: endpoint,
		Token: token,
		CacheDir: expandedCache,
		UserAgent: "huggingface-go/0.0.1",
	}
}


type DownloadParams struct {
	Repo        	*Repo
	FileName    	string
	SubFolder   	string
	Revision    	string
	ForceDownload 	bool
	LocalFilesOnly 	bool
}

type Repo struct {
	Id       string
	Type     string
	Revision string
}

type FileMetadata struct {
	CommitHash string
	ETag       string
	Location   string
	Size       int
}

