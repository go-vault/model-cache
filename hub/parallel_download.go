package hub

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
    "net"

	"github.com/cenkalti/backoff/v4"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)


type parallelDownloader struct {
    progress *mpb.Progress
    wg       sync.WaitGroup
    errors   chan error
    totalFiles int
    downloadedFiles atomic.Int32
    totalBar *mpb.Bar
}


func newParallelDownloader(client *Client, totalFiles int, repoId string) *parallelDownloader {
    pd := &parallelDownloader{
        progress: client.Progress,
        errors: make(chan error, 100),
        totalFiles: totalFiles,
    }


    pd.totalBar = pd.progress.AddBar(
        int64(totalFiles),
        mpb.BarRemoveOnComplete(),
        mpb.PrependDecorators(
            decor.Name(fmt.Sprintf("Fetching %d files for %s:", totalFiles, repoId), decor.WC{W: len(fmt.Sprint(totalFiles)) + 20}),
            decor.CountersNoUnit("%d/%d", decor.WCSyncWidth),
        ),
        mpb.AppendDecorators(
            decor.NewPercentage("%d ", decor.WCSyncSpace),
        ),
    )

    return pd
}


func (pd *parallelDownloader) downloadFile(client *Client, params *DownloadParams) {
    pd.wg.Add(1)
    go func() {
        defer pd.wg.Done()

        storageFolder := filepath.Join(
            client.CacheDir,
            repoFolderName(params.Repo.Id, params.Repo.Type),
        )

        // metadata to check if file exists
        headers := getHeaders(client)

        metadata, err := getFileMetadata(client, params.Repo.Id, params.FileName, headers)
        if err != nil {
            pd.errors <- fmt.Errorf("failed to get metadata for %s: %w", params.FileName, err)
            return
        }

        pointerPath := filepath.Join(storageFolder, "snapshots", metadata.CommitHash, params.FileName)
        blobPath := filepath.Join(storageFolder, "blobs", metadata.ETag)

        // check if file already exists and we're not forcing download
        if !params.ForceDownload {
            if _, err := os.Stat(pointerPath); err == nil {
                pd.downloadedFiles.Add(1)
                pd.totalBar.Increment()
                return
            }
            if _, err := os.Stat(blobPath); err == nil {
                // blob exists but pointer doesn't exist - create the pointer
                os.MkdirAll(filepath.Dir(pointerPath), 0755)
                if err := createSymlink(blobPath, pointerPath); err != nil {
                    pd.errors <- fmt.Errorf("failed to create symlink for %s: %w", params.FileName, err)
                    return
                }
                pd.downloadedFiles.Add(1)
                pd.totalBar.Increment()
                return
            }
        }


        bar := pd.progress.AddBar(
            int64(metadata.Size),
            mpb.BarRemoveOnComplete(),
            mpb.PrependDecorators(
                decor.Name(params.FileName, decor.WC{W: 50, C: decor.DidentRight}),
                decor.Percentage(decor.WCSyncSpace),
            ),
            mpb.AppendDecorators(
                decor.CountersKibiByte("%.2f / %.2f", decor.WCSyncWidth),
                decor.Name(" | ", decor.WCSyncSpace),
                decor.AverageSpeed(decor.UnitKB, "%.2f", decor.WCSyncSpace),
            ),
            mpb.BarWidth(70),
        )


        if _, err := pd.downloadSingleFile(client, params, bar, metadata); err != nil {
            pd.errors <- fmt.Errorf("failed to download %s: %w", params.FileName, err)
            bar.Abort(true)
            return
        }

        pd.downloadedFiles.Add(1)
        pd.totalBar.Increment()
    }()
}


func (pd *parallelDownloader) downloadSingleFile(client *Client, params *DownloadParams, bar *mpb.Bar, metadata *FileMetadata) (string, error) {

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
    headers := &http.Header{}
    headers.Set("User-Agent", client.UserAgent)
    if client.Token != "" {
        headers.Set("Authorization", "Bearer "+client.Token)
    }

    // Backoff and retry logic
    b := backoff.NewExponentialBackOff()
    b.MaxElapsedTime = 5 * time.Minute
    b.InitialInterval = 1 * time.Second
    b.MaxInterval = 30 * time.Second

    err := backoff.Retry(func() error {
        return downloadWithBar(metadata.Location, tmpPath, headers, bar)
    }, b)

    if err != nil {
        return "", fmt.Errorf("failed after retries: %w", err)
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
    defer func() {
        out.Sync()
        out.Close()
    }()

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
        Timeout: 0,
        Transport: &http.Transport{
            DialContext: (&net.Dialer{
                Timeout: 60 * time.Second,
            }).DialContext,
            TLSHandshakeTimeout:   60 * time.Second,
            ResponseHeaderTimeout: 60 * time.Second,
            IdleConnTimeout:       60 * time.Second,
        },
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

    stallTimer := time.Duration(0)
    lastUpdate := time.Now()

    for {
        n, err := reader.Read(buf)
        if n > 0 {
            if _, werr := out.Write(buf[:n]); werr != nil {
                return werr
            }
            bar.IncrBy(n)

            now := time.Now()
            if now.Sub(lastUpdate) > 30*time.Second {
                stallTimer += now.Sub(lastUpdate)
                if stallTimer > 2*time.Minute {
                    return fmt.Errorf("download stalled for too long")
                }
            } else {
                stallTimer = 0
                lastUpdate = now
            }
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

func (pd *parallelDownloader) Wait() {
    pd.wg.Wait()
    close(pd.errors)
    pd.totalBar.SetTotal(int64(pd.totalFiles), true)
    
    // wait for progress bars to complete rendering
    // pd.progress.Wait()
}
