package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cozy-creator/hf-hub/hub"
)


func isBoolean(data json.RawMessage) bool {
    var b bool
    return json.Unmarshal(data, &b) == nil
}

func (m *ModelIndex) UnmarshalJSON(data []byte) error {
    // Temporary struct to capture the basic fields
    type tempIndex struct {
        ClassName         string `json:"_class_name"`
        DiffusersVersion string `json:"_diffusers_version,omitempty"`
    }

    // Parse into map to get all fields
    var rawMap map[string]json.RawMessage
    if err := json.Unmarshal(data, &rawMap); err != nil {
        return err
    }

    // Parse the basic fields
    var temp tempIndex
    if err := json.Unmarshal(data, &temp); err != nil {
        return err
    }
    m.ClassName = temp.ClassName
    m.DiffusersVersion = temp.DiffusersVersion

    // Initialize components map
    m.Components = make(map[string][]string)

    // Parse each field that's not prefixed with "_" as a component or check if type is boolean
    for key, value := range rawMap {
        if !strings.HasPrefix(key, "_") && !isBoolean(value) {
            var component []string
            if err := json.Unmarshal(value, &component); err != nil {
                return err
            }
            m.Components[key] = component
        }
    }

    return nil
}


type DiffusionPipelineDownloader struct {
	client *hub.Client
}


func NewDiffusionPipelineDownloader(client *hub.Client) *DiffusionPipelineDownloader {
	return &DiffusionPipelineDownloader{
		client: client,
	}
}


func (dpd *DiffusionPipelineDownloader) Download(repoID string, variant string, opts *DownloadOptions) (string, error) {
	if opts == nil {
		opts = &DownloadOptions{
			UseSafetensors: false,
		}
	}

	// download the model index first
	params := &hub.DownloadParams{
		Repo: &hub.Repo{
			Id: repoID,
			Type: hub.ModelRepoType,
		},
		FileName: "model_index.json",
	}

	modelIndexPath, err := dpd.client.Download(params)
	if err != nil {
		return "", fmt.Errorf("failed to get model index: %w", err)
	}

	// parse the model index
	modelIndex, err := dpd.parseModelIndex(modelIndexPath)
	if err != nil {
		return "", fmt.Errorf("failed to parse model index: %w", err)
	}


	// try downloading with format hierarchy
	var lastErr error
	if opts.UseSafetensors {
		// only try safetensors
		snapshotPath, err := dpd.tryDownloadFormat(repoID, modelIndex, variant, ".safetensors")
		if err != nil {
			return "", fmt.Errorf("safetensors required but not available: %w", err)
		}
		return snapshotPath, nil
	}

	// try formats in order of preference
	formats := []string{
		".safetensors",
		".ckpt",
		".bin",
	}

	for _, format := range formats {
		snapshotPath, err := dpd.tryDownloadFormat(repoID, modelIndex, variant, format)
		if err == nil {
			return snapshotPath, nil
		}
		lastErr = err
	}



	return "", fmt.Errorf("no compatible model format found: %w", lastErr)
}


func (dpd *DiffusionPipelineDownloader) tryDownloadFormat(repoID string, modelIndex *ModelIndex, variant string, format string) (string, error) {
	patterns := dpd.buildDownloadPatterns(modelIndex, variant, format)

	params := &hub.DownloadParams{
		Repo: &hub.Repo{
			Id: repoID,
			Type: hub.ModelRepoType,
		},
		AllowPatterns: patterns,
	}

	snapshotPath, err := dpd.client.Download(params)
	if err != nil {
		return "", fmt.Errorf("failed to download model in %s format: %w", format, err)
	}

	// verify at least one weight file was downloaded
	hasWeights := false
	for component := range modelIndex.Components {
		pattern := filepath.Join(snapshotPath, component, "*."+format)
		if variant != "" {
			pattern = filepath.Join(snapshotPath, component, "*."+variant+format)
		}
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			hasWeights = true
			break
		}
	}

	if !hasWeights {
		return "", fmt.Errorf("no weight files found in %s format", format)
	}


	// download connected pipelines, if any
	if err := dpd.downloadConnectedPipelines(modelIndex, variant); err != nil {
		return "", fmt.Errorf("failed to download connected pipelines: %w", err)
	}

	return snapshotPath, nil
}


func (dpd *DiffusionPipelineDownloader) parseModelIndex(path string) (*ModelIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read model index: %w", err)
	}

	var index ModelIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model index: %w", err)
	}

	return &index, nil
}


func (dpd *DiffusionPipelineDownloader) buildDownloadPatterns(index *ModelIndex, variant string, format string) []string {
	patterns := []string{}

	for componentName := range index.Components {

		// add component's config files
        patterns = append(patterns,
			fmt.Sprintf("%s/*.json", componentName),
		)

		// for tokenizers and schedulers, download everything
		if strings.Contains(componentName, "tokenizer") || strings.Contains(componentName, "scheduler") {
			patterns = append(patterns, fmt.Sprintf("%s/*", componentName))
			continue
		}


        // For other components, follow variant and format patterns
        baseNames := []string{
            "diffusion_pytorch_model",
            "model",
            "pytorch_model",
        }

        for _, baseName := range baseNames {
            if variant == "" {
                // Base patterns for weights
                patterns = append(patterns,
                    // Regular files
                    fmt.Sprintf("%s/%s%s", componentName, baseName, format),
                    // Sharded files
                    fmt.Sprintf("%s/%s-[0-9][0-9][0-9][0-9][0-9]-of-[0-9][0-9][0-9][0-9][0-9]%s", componentName, baseName, format),
                )
            } else {
                // Variant patterns for weights
                patterns = append(patterns,
                    // Regular files
                    fmt.Sprintf("%s/%s.%s%s", componentName, baseName, variant, format),
					// Sharded files (current format)
                    fmt.Sprintf("%s/%s.%s-[0-9][0-9][0-9][0-9][0-9]-of-[0-9][0-9][0-9][0-9][0-9]%s", componentName, baseName, variant, format),
                    // Sharded files (deprecated format)
                    fmt.Sprintf("%s/%s-[0-9][0-9][0-9][0-9][0-9]-of-[0-9][0-9][0-9][0-9][0-9].%s%s", componentName, baseName, variant, format),
                )
            }
        }
    }

	return patterns
}


func (dpd *DiffusionPipelineDownloader) downloadConnectedPipelines(index *ModelIndex, variant string) error {
	if len(index.ConnectedPipes) == 0 {
		return nil
	}

	for _, connectedRepo := range index.ConnectedPipes {
		if _, err := dpd.Download(connectedRepo, variant, nil); err != nil {
			return fmt.Errorf("failed to download connected pipeline %s: %w", connectedRepo, err)
		}
	}

	return nil
}
