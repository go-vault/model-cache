package hub

import (
    "net/http"
    "os"
    "fmt"
    "sync"
    "time"
    "path/filepath"
    "bufio"
    "io"
    // "strings"

    "github.com/vbauerster/mpb/v8"
    "github.com/vbauerster/mpb/v8/decor"
)


type parallelDownloader struct {
    progress *mpb.Progress
    wg       sync.WaitGroup
    errors   chan error
}


func newParallelDownloader() *parallelDownloader {
    return &parallelDownloader{
        progress: mpb.New(
            mpb.WithWidth(60),
            mpb.WithRefreshRate(180*time.Millisecond),
        ),
        errors: make(chan error, 100),
    }
}


func (pd *parallelDownloader) downloadFile(client *Client, params *DownloadParams) {
    pd.wg.Add(1)
    go func() {
        defer pd.wg.Done()

        bar := pd.progress.AddBar(
            -1, // Update total when we get metadata,
            mpb.PrependDecorators(
                decor.Name(params.FileName, decor.WC{W: 45, C: decor.DindentRight}),
                decor.Percentage(decor.WCSyncSpace),
                decor.Name("|", decor.WCSyncSpace),
            ),
            mpb.AppendDecorators(
                decor.CountersKibiByte("% .2f / % .2f"),

                // add space between speed and percentage
                decor.Name("|", decor.WCSyncSpace),
                decor.AverageSpeed(decor.SizeB1024(0), "% .2f"),
            ),
        )

        if _, err := pd.downloadSingleFile(client, params, bar); err != nil {
            pd.errors <- fmt.Errorf("failed to download %s: %w", params.FileName, err)
            bar.Abort(true)
        }
    }()
}


func (pd *parallelDownloader) downloadSingleFile(client *Client, params *DownloadParams, bar *mpb.Bar) (string, error) {
    // Get metadata first
    headers := &http.Header{}
    headers.Set("User-Agent", client.UserAgent)
    if client.Token != "" {
        headers.Set("Authorization", "Bearer "+client.Token)
    }

    metadata, err := getFileMetadata(client, params.Repo.Id, params.FileName, headers)
    if err != nil {
        return "", err
    }

    // Set the total size for progress bar
    bar.SetTotal(int64(metadata.Size), false)


    storageFolder := filepath.Join(
        client.CacheDir,
        repoFolderName(params.Repo.Id, params.Repo.Type),
    )

    blobPath := filepath.Join(storageFolder, "blobs", metadata.ETag)
    pointerPath := filepath.Join(storageFolder, "snapshots", metadata.CommitHash, params.FileName)

    os.MkdirAll(filepath.Dir(blobPath), 0755)
    os.MkdirAll(filepath.Dir(pointerPath), 0755)

    // Download with progress
    tmpPath := blobPath + ".incomplete"
    if err := downloadWithBar(metadata.Location, tmpPath, headers, bar); err != nil {
        return "", err
    }

    // Move to final location
    if err := os.Rename(tmpPath, blobPath); err != nil {
        return "", err
    }

    if err := createSymlink(blobPath, pointerPath); err != nil {
        return "", err
    }

    return pointerPath, nil
}

func downloadWithBar(url string, destPath string, headers *http.Header, bar *mpb.Bar) error {
    // Resume logic
    var resumeSize int64 = 0
    if stat, err := os.Stat(destPath); err == nil {
        resumeSize = stat.Size()
        bar.SetCurrent(resumeSize)
    }

    flag := os.O_CREATE | os.O_WRONLY
    if resumeSize > 0 {
        flag |= os.O_APPEND
    }

    out, err := os.OpenFile(destPath, flag, 0644)
    if err != nil {
        return err
    }
    defer out.Close()

    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return err
    }

    if headers != nil {
        req.Header = *headers
    }

    // Add range header for resume
    if resumeSize > 0 {
        req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeSize))
    }

    client := &http.Client{
        Timeout: time.Minute * 30,
    }

    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Handle resume
    if resumeSize > 0 && resp.StatusCode != http.StatusPartialContent {
        resumeSize = 0
        out.Seek(0, 0)
        out.Truncate(0)
        bar.SetCurrent(0)
    }

    if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
        return fmt.Errorf("bad status: %s", resp.Status)
    }

    // Copy data with progress
    reader := bufio.NewReader(resp.Body)
    buf := make([]byte, 32*1024)

    for {
        n, err := reader.Read(buf)
        if n > 0 {
            if _, werr := out.Write(buf[:n]); werr != nil {
                return werr
            }
            bar.IncrBy(n)
        }

        if err == io.EOF {
            break
        }
        if err != nil {
            return err
        }
    }

    return nil
}

// truncateFilename helper
// func truncateFilename(name string, maxLen int) string {
//     if len(name) <= maxLen {
//         return name + strings.Repeat(" ", maxLen-len(name))
//     }
//     return "..." + name[len(name)-(maxLen-3):]
// }