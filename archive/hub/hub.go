package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Client struct {
	Endpoint  string
	Token     string
	CacheDir  string
	UserAgent string
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

type ModelInfo struct {
	Sha      string             `json:"sha"`
	Siblings []ModelInfoSibling `json:"siblings"`
}

type ModelInfoSibling struct {
	RFileName string `json:"rfilename"`
}

const (
	DefaultRevision  = "main"
	DefaultCacheDir  = "~/.cache/huggingface/hub"
	DefaultUserAgent = "unknown/None; hf-hub/0.0.1"
)

const (
	ModelRepoType   = "model"
	SpaceRepoType   = "space"
	DatasetRepoType = "dataset"
)

const (
	defaultHfEndpoint           = "https://huggingface.co"
	defaultHfStagingEndpoint    = "https://hub-ci.huggingface.co"
	hfResolveUrlTemplate        = "{{.Endpoint}}/{{.RepoId}}/resolve/{{.Revision}}/{{.Filename}}"
	hfModelInfoTemplate         = "{{.Endpoint}}/api/models/{{.RepoId}}"
	hfModelRevisionInfoTemplate = "{{.Endpoint}}/api/models/{{.RepoId}}/revision/{{.Revision}}"
)

const (
	HeaderFilenamePattern = `filename=\"(?P<filename>.*?)\";`
	CommitHashPattern     = "^[0-9a-f]{40}$"
)

var IsStaging bool
var HFEndpoint = os.Getenv("HF_ENDPOINT")

const DownloadChunkSize = 1024 * 1024
const DefaultRetries = 5

var RepoTypes = []string{ModelRepoType, SpaceRepoType, DatasetRepoType}
var RepoTypesUrlPrefixes = map[string]string{
	SpaceRepoType:   "spaces/",
	DatasetRepoType: "datasets/",
}

var symlinkSupported map[string]bool

type DownloadParams struct {
	Repo           *Repo
	FileName       string
	SubFolder      string
	Revision       string
	ForceDownload  bool
	LocalFilesOnly bool
}

func init() {
	isStaging, err := strconv.ParseBool(os.Getenv("HUGGINGFACE_CO_STAGING"))
	if err != nil {
		isStaging = false
	}

	IsStaging = isStaging
	if HFEndpoint == "" {
		if isStaging {
			HFEndpoint = defaultHfStagingEndpoint
		} else {
			HFEndpoint = defaultHfEndpoint
		}
	}
}

func NewClient(endpoint string, token string, cacheDir string) *Client {
	cacheDir, err := expandPath(cacheDir)
	if err != nil {
		panic(err)
	}
	
	return &Client{
		Endpoint: endpoint,
		Token:    token,
		CacheDir: cacheDir,
	}
}

func DefaultClient() *Client {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir != "" {
		cacheDir = filepath.Join(cacheDir, "huggingface")
	}

	if cacheDir == "" {
		cacheDir = os.Getenv("HF_HUB_CACHE")
	}

	if cacheDir == "" {
		cacheDir = os.Getenv("HF_HOME")
		if cacheDir != "" {
			cacheDir = filepath.Join(cacheDir, "hub")
		}
	}

	if cacheDir == "" {
		cacheDir = DefaultCacheDir
	}

	cacheDir, err := expandPath(cacheDir)
	if err != nil {
		panic(err)
	}

	return &Client{
		Endpoint:  HFEndpoint,
		CacheDir:  cacheDir,
		UserAgent: DefaultUserAgent,
	}
}

func NewRepo(repoId string) *Repo {
	return &Repo{
		Id:       repoId,
		Type:     ModelRepoType,
		Revision: DefaultRevision,
	}
}

func (r *Repo) WithRevision(revision string) *Repo {
	r.Revision = revision
	return r
}

func (r *Repo) WithType(repoType string) *Repo {
	r.Type = repoType
	return r
}

func (client *Client) WithCacheDir(cacheDir string) *Client {
	client.CacheDir = cacheDir
	return client
}

func (client *Client) WithEndpoint(endpoint string) *Client {
	client.Endpoint = endpoint
	return client
}

func (client *Client) WithToken(token string) *Client {
	client.Token = token
	return client
}

func (client *Client) WithUserAgent(userAgent string) *Client {
	client.UserAgent = userAgent
	return client
}

func (client *Client) Download(params *DownloadParams) (string, error) {
	if params.Repo.Type == "" {
		params.Repo.Type = ModelRepoType
	}

	if params.Repo.Revision == "" {
		params.Repo.Revision = DefaultRevision
	}

	if params.Revision == "" {
		params.Revision = params.Repo.Revision
	}

	if client.CacheDir == "" {
		client.CacheDir = DefaultCacheDir
	}

	if params.FileName == "" {
		return fileDownload(client, params)
	} else {
		if params.Repo.Type != ModelRepoType {
			return "", fmt.Errorf("invalid repo type: %s", params.Repo.Type)
		}
		return snapshotDownload(client, params)
	}
}
