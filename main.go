package main

import (
	"fmt"
	"log"

	"github.com/cozy-creator/hf-hub/hub"
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

func main() {

	client := hub.DefaultClient()

	params := &hub.DownloadParams{
		Repo: &hub.Repo{
			Id: "stable-diffusion-v1-5/stable-diffusion-v1-5",
			Type: hub.ModelRepoType,
		},
		AllowPatterns: []string{
			// "*.json",
			// "*.txt",
			"text_encoder/*",
		},

		// IgnorePatterns: []string{
		// 	"*.bin",
		// 	"*model.safetensors",
		// 	"*balstadar",
		// },
	}

	path, err := client.Download(params)
	if err != nil {
		log.Fatalf("Error downloading file: %v", err)
	}

	fmt.Println("File downloaded to:", path)
}

