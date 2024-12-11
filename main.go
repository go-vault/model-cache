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
    
    // Download a diffusion model
    fmt.Println("Starting download...")
    modelPath, err := downloader.Download("stabilityai/stable-diffusion-3.5-large", "", &pipeline.DownloadOptions{
        UseSafetensors: true,
    })
    if err != nil {
        log.Fatalf("Failed to download model: %v", err)
    }

    // Wait for progress bars to finish
    progress.Wait()

    fmt.Printf("Model downloaded to: %s\n", modelPath)
}

// func getHeaders(client *hub.Client) *http.Header {
// 	headers := &http.Header{}
// 	headers.Set("User-Agent", client.UserAgent)
// 	// fmt.Println("client.Token", client.Token)
// 	if client.Token != "" {
// 		headers.Set("Authorization", "Bearer "+client.Token)
// 	}
// 	return headers
// }

// func main() {
//     client := hub.DefaultClient()
//     client.Progress = mpb.New(
//         mpb.WithWidth(60),
//         mpb.WithRefreshRate(180*time.Millisecond),
//     )

//     // Verify token
//     fmt.Printf("Token present: %v\n", client.Token != "")
    
//     // Try to get model info first to verify authentication
//     url := fmt.Sprintf("%s/api/models/%s", client.Endpoint, "stabilityai/stable-diffusion-3.5-large")
//     req, err := http.NewRequest("GET", url, nil)
//     if err != nil {
//         log.Fatalf("Failed to create request: %v", err)
//     }
    
//     headers := getHeaders(client)
//     req.Header = *headers
    
//     resp, err := http.DefaultClient.Do(req)
//     if err != nil {
//         log.Fatalf("Failed to make request: %v", err)
//     }
//     defer resp.Body.Close()
    
//     fmt.Printf("Model info response status: %s\n", resp.Status)
    
//     if resp.StatusCode == 401 {
//         body, _ := io.ReadAll(resp.Body)
//         fmt.Printf("Auth error response: %s\n", string(body))
//         return
//     }

//     // If we get here, auth is working
//     downloader := pipeline.NewDiffusionPipelineDownloader(client)
    
//     fmt.Println("Starting download...")
//     modelPath, err := downloader.Download("stabilityai/stable-diffusion-3.5-large", "", &pipeline.DownloadOptions{
//         UseSafetensors: true,
//     })
//     if err != nil {
//         // Print more detailed error info
//         if strings.Contains(err.Error(), "401") {
//             fmt.Printf("Authentication error occurred during download. Full error: %v\n", err)
//             fmt.Printf("Token being used: %s...[truncated]\n", client.Token[:10])
//         } else {
//             log.Fatalf("Failed to download model: %v", err)
//         }
//         return
//     }

//     client.Progress.Wait()
//     fmt.Printf("Model downloaded to: %s\n", modelPath)
// }