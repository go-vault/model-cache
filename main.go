package main

import (
    "fmt"
    "log"
    "time"
	// "io"
	// "net/http"
	// "strings"

    "github.com/cozy-creator/hf-hub/hub"
    "github.com/cozy-creator/hf-hub/hub/pipeline"
    "github.com/vbauerster/mpb/v7"
)

// test file download

// func main() {

// 	client := hub.DefaultClient()

// 	params := &hub.DownloadParams{
// 		Repo: &hub.Repo{
// 			Id: "stable-diffusion-v1-5/stable-diffusion-v1-5",
// 			Type: hub.ModelRepoType,
// 		},
// 		FileName: "unet/diffusion_pytorch_model.safetensors",
// 	}

// 	path, err := client.Download(params)
// 	if err != nil {
// 		log.Fatalf("Error downloading file: %v", err)
// 	}

// 	fmt.Println("File downloaded to:", path)
// }


// test snapshot download

// func main() {

// 	client := hub.DefaultClient()

// 	params := &hub.DownloadParams{
// 		Repo: &hub.Repo{
// 			Id: "stable-diffusion-v1-5/stable-diffusion-v1-5",
// 			Type: hub.ModelRepoType,
// 		},
// 		AllowPatterns: []string{
// 			// "*.json",
// 			// "*.txt",
// 			"text_encoder/*",
// 		},

// 		// IgnorePatterns: []string{
// 		// 	"*.bin",
// 		// 	"*model.safetensors",
// 		// 	"*balstadar",
// 		// },
// 	}

// 	path, err := client.Download(params)
// 	if err != nil {
// 		log.Fatalf("Error downloading file: %v", err)
// 	}

// 	fmt.Println("File downloaded to:", path)
// }


// Diffusion pipeline download

func main() {
    // Create default client
    client := hub.DefaultClient()
    
    // Initialize progress bar
    progress := mpb.New(
        mpb.WithWidth(60),
        mpb.WithRefreshRate(180*time.Millisecond),
    )
    client.Progress = progress

    downloader := pipeline.NewDiffusionPipelineDownloader(client)
    
    // Download a diffusion model, ignore text_encoder
    fmt.Println("Starting download...")
    modelPath, err := downloader.Download("fal/AuraFlow-v0.3", "", nil, nil)
    if err != nil {
        log.Fatalf("Failed to download model: %v", err)
    }

    // Wait for progress bars to finish
    progress.Wait()

    fmt.Printf("Model downloaded to: %s\n", modelPath)
}

