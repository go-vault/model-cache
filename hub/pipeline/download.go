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


func (dpd *DiffusionPipelineDownloader) Download(repoID string, variant string, opts *DownloadOptions, components map[string]*hub.ComponentDef) (string, error) {
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
		snapshotPath, err := dpd.tryDownloadFormat(repoID, modelIndex, variant, ".safetensors", components)
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
		snapshotPath, err := dpd.tryDownloadFormat(repoID, modelIndex, variant, format, components)
		if err == nil {
			return snapshotPath, nil
		}
		lastErr = err
	}



	return "", fmt.Errorf("no compatible model format found: %w", lastErr)
}


func (dpd *DiffusionPipelineDownloader) tryDownloadFormat(repoID string, modelIndex *ModelIndex, variant string, format string, components map[string]*hub.ComponentDef) (string, error) {
	patterns := dpd.buildDownloadPatterns(modelIndex, variant, format, components)

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

	ignoredFolders := map[string]bool{
        "scheduler":          true,
        "tokenizer":         true,
        "tokenizer_2":       true,
        "tokenizer_3":       true,
        "feature_extractor": true,
        "safety_checker":    true,
        "image_encoder":     true,
    }

	missingComponents := []string{}
    for component := range modelIndex.Components {
		// skip ignored components
		if ignoredFolders[component] {
			continue
		}
        componentPath := filepath.Join(snapshotPath, component)
        
        // Check if component directory exists
        if _, err := os.Stat(componentPath); os.IsNotExist(err) {
            missingComponents = append(missingComponents, component)
            continue
        }

        // List files in component directory
        files, err := os.ReadDir(componentPath)
        if err != nil {
            missingComponents = append(missingComponents, component)
            continue
        }

        // Build pattern for matching
        var pattern string
        if variant != "" {
            pattern = "*." + variant + format
        } else {
            pattern = "*" + format
        }

        // Check if component has weights
        hasComponentWeights := false
        for _, file := range files {
            if !file.IsDir() {
                matched, err := filepath.Match(pattern, file.Name())
                if err != nil {
                    continue
                }
                if matched {
                    hasComponentWeights = true
                    break
                }
            }
        }

        if !hasComponentWeights {
            missingComponents = append(missingComponents, component)
        }
    }

    if len(missingComponents) > 0 {
        return "", fmt.Errorf("missing weights for components in %s format: %v", format, missingComponents)
    }

	// download connected pipelines, if any
	if err := dpd.downloadConnectedPipelines(modelIndex, variant); err != nil {
		return "", fmt.Errorf("failed to download connected pipelines: %w", err)
	}

    return snapshotPath, nil
}

// func listDirFiles(dir string) []string {
//     files, err := os.ReadDir(dir)
//     if err != nil {
//         return nil
//     }
//     var fileNames []string
//     for _, file := range files {
//         if !file.IsDir() {
//             fileNames = append(fileNames, file.Name())
//         }
//     }
//     return fileNames
// }


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


func (dpd *DiffusionPipelineDownloader) buildDownloadPatterns(index *ModelIndex, variant string, format string, components map[string]*hub.ComponentDef) []string {
	patterns := []string{}

    // Get list of component folders to ignore
    ignoreComponents := make(map[string]bool)
    if components != nil {
        for compName := range components {
            ignoreComponents[compName] = true
        }
    }

	for componentName := range index.Components {

		// skip ignored components
		if ignoreComponents[componentName] {
			continue
		}

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
		if _, err := dpd.Download(connectedRepo, variant, nil, nil); err != nil {
			return fmt.Errorf("failed to download connected pipeline %s: %w", connectedRepo, err)
		}
	}

	return nil
}
