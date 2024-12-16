// hub/download.go
package hub

import (
   "fmt"
   "io"
   "net/http"
   "net/url"
   "os"
   "path/filepath"
   "regexp"
   "time"
   "net"
   "sync"
   "log"
   
   "github.com/vbauerster/mpb/v7"
   "github.com/vbauerster/mpb/v7/decor"
   "github.com/cenkalti/backoff/v4"
)

type DownloadSource interface {
   GetFileInfo() (*FileInfo, error)
   Download(destPath string, progress *mpb.Progress) error 
}

type FileInfo struct {
   URL       string
   Size      int64
   Filename  string
}

type CivitaiSource struct {
   url       string
   apiKey    string
   progressMu sync.Mutex
}

type DirectURLSource struct {
   url       string
   progressMu sync.Mutex
}

func NewCivitaiSource(url string, apiKey string) *CivitaiSource {
   return &CivitaiSource{url: url, apiKey: apiKey}
}

func (s *CivitaiSource) GetFileInfo() (*FileInfo, error) {
   client := &http.Client{
       CheckRedirect: func(req *http.Request, via []*http.Request) error {
           return http.ErrUseLastResponse
       },
       Timeout: 30 * time.Second,
   }

   req, err := http.NewRequest("GET", s.url, nil)
   if err != nil {
       return nil, err
   }

   if s.apiKey != "" {
       req.Header.Set("Authorization", "Bearer " + s.apiKey)
   }

   resp, err := client.Do(req)
   if err != nil {
       return nil, err 
   }
   defer resp.Body.Close()

   if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusTemporaryRedirect {
       return nil, fmt.Errorf("expected redirect response, got status %d", resp.StatusCode)
   }

   location := resp.Header.Get("Location")
   if location == "" {
       return nil, fmt.Errorf("no redirect location found")
   }

   redirectURL, err := url.Parse(location)
   if err != nil {
       return nil, fmt.Errorf("failed to parse redirect location: %w", err)
   }

   var filename string
   queryParams := redirectURL.Query()
   if contentDisp := queryParams.Get("response-content-disposition"); contentDisp != "" {
       re := regexp.MustCompile(`filename="([^"]+)`)
       if matches := re.FindStringSubmatch(contentDisp); len(matches) > 1 {
           filename = matches[1]
       }
   }

   if filename == "" && redirectURL.Path != "" {
       filename = filepath.Base(redirectURL.Path)
   }

   return &FileInfo{
       URL: location,
       Size: resp.ContentLength,
       Filename: filename,
   }, nil
}

func (s *CivitaiSource) Download(destPath string, progress *mpb.Progress) error {
   tmpPath := destPath + ".tmp"
   
   b := backoff.NewExponentialBackOff()
   b.MaxElapsedTime = 5 * time.Minute  
   b.InitialInterval = 1 * time.Second
   b.MaxInterval = 30 * time.Second

   return backoff.Retry(func() error {
       if err := downloadWithResume(s.url, destPath, tmpPath, s.apiKey, progress, &s.progressMu); err != nil {
           log.Printf("[Download] Retry error: %v", err)
           return err
       }
       return nil
   }, b)
}

func NewDirectURLSource(url string) *DirectURLSource {
   return &DirectURLSource{url: url}
}

func (s *DirectURLSource) GetFileInfo() (*FileInfo, error) {
   return &FileInfo{
       URL: s.url,
       Filename: filepath.Base(s.url),
   }, nil
}

func (s *DirectURLSource) Download(destPath string, progress *mpb.Progress) error {
   tmpPath := destPath + ".tmp"
   
   b := backoff.NewExponentialBackOff()
   b.MaxElapsedTime = 5 * time.Minute
   b.InitialInterval = 1 * time.Second
   b.MaxInterval = 30 * time.Second

   return backoff.Retry(func() error { 
       return downloadWithResume(s.url, destPath, tmpPath, "", progress, &s.progressMu)
   }, b)
}

func downloadWithResume(url, destPath, tmpPath, apiKey string, progress *mpb.Progress, progressMu *sync.Mutex) error {
   var initialSize int64 = 0
   if info, err := os.Stat(tmpPath); err == nil {
       initialSize = info.Size()
   }

   flag := os.O_CREATE | os.O_WRONLY
   if initialSize > 0 {
       flag |= os.O_APPEND
   }

   out, err := os.OpenFile(tmpPath, flag, 0644)
   if err != nil {
       return err
   }
   defer func() {
       out.Sync()
       out.Close()
   }()

   client := &http.Client{
       Timeout: 0,
       Transport: &http.Transport{
           DialContext: (&net.Dialer{
               Timeout: 60 * time.Second,
           }).DialContext,
           TLSHandshakeTimeout: 60 * time.Second,
           ResponseHeaderTimeout: 60 * time.Second,
           IdleConnTimeout: 60 * time.Second,
       },
   }

   req, err := http.NewRequest("GET", url, nil)
   if err != nil {
       return fmt.Errorf("failed to create request: %w", err)
   }

   if apiKey != "" {
       req.Header.Set("Authorization", "Bearer " + apiKey)
   }

   if initialSize > 0 {
       req.Header.Set("Range", fmt.Sprintf("bytes=%d-", initialSize))
   }

   resp, err := client.Do(req)
   if err != nil {
       log.Printf("[Download] Request failed for URL %s: %v", url, err)
       fmt.Printf("[Download] Request failed for URL %s: %v", url, err)
       return fmt.Errorf("request failed: %w", err)
   }
   defer resp.Body.Close()

   var totalSize int64
   if initialSize > 0 {
       if resp.StatusCode == http.StatusPartialContent {
           totalSize = initialSize + resp.ContentLength
       } else if resp.StatusCode == http.StatusOK {
           initialSize = 0
           out.Seek(0, 0)
           out.Truncate(0)
           totalSize = resp.ContentLength
       } else {
           log.Printf("resume failed with status %d", resp.StatusCode)
           fmt.Printf("resume failed with status (fmt) %d", resp.StatusCode)
           return fmt.Errorf("resume failed with status %d", resp.StatusCode)
       }
   } else {
       if resp.StatusCode != http.StatusOK {
           log.Printf("download failed with status %d", resp.StatusCode)
           fmt.Printf("download failed with status (fmt) %d", resp.StatusCode)
           return fmt.Errorf("download failed with status %d", resp.StatusCode)
       }
       totalSize = resp.ContentLength
   }

   progressMu.Lock()
   bar := progress.AddBar(totalSize,
       mpb.BarRemoveOnComplete(),
       mpb.PrependDecorators(
           decor.Name(filepath.Base(destPath), decor.WC{W: 40, C: decor.DidentRight}),
           decor.CountersKibiByte("% .2f / % .2f"),
       ),
       mpb.AppendDecorators(
           decor.EwmaETA(decor.ET_STYLE_GO, 90),
           decor.Name(" ] "),
           decor.EwmaSpeed(decor.UnitKiB, "% .2f", 60),
       ),
   )
   progressMu.Unlock()

   if initialSize > 0 {
       bar.SetCurrent(initialSize)
   }

   downloadedSize := initialSize
   lastUpdate := time.Now()
   stallTimer := time.Duration(0)

   reader := bar.ProxyReader(resp.Body)
   defer reader.Close()

   buf := make([]byte, 32*1024)

   for {
       n, err := reader.Read(buf)
       if n > 0 {
           if _, werr := out.Write(buf[:n]); werr != nil {
               return fmt.Errorf("write failed: %w", werr)
           }

           downloadedSize += int64(n)

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
           return fmt.Errorf("read failed: %w", err)
       }
   }

   if totalSize > 0 && downloadedSize != totalSize {
       return fmt.Errorf("download size mismatch: expected %d, got %d", totalSize, downloadedSize)
   }

   out.Close()

   if err := os.Rename(tmpPath, destPath); err != nil {
       return fmt.Errorf("failed to move file: %w", err)
   }

   return nil
}