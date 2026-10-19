package workerimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var sumLine = regexp.MustCompile(`^([a-f0-9]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)$`)

func parseSums(data []byte) (map[string]string, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, errors.New("invalid checksum manifest size")
	}
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		match := sumLine.FindStringSubmatch(line)
		if match == nil {
			return nil, errors.New("invalid checksum manifest entry")
		}
		if _, exists := result[match[2]]; exists {
			return nil, errors.New("duplicate checksum manifest entry")
		}
		result[match[2]] = match[1]
	}
	return result, nil
}
func downloadVerified(ctx context.Context, opts options, name, target, digest string, max int64, log func(string)) error {
	if err := download(ctx, opts, name, target, max, log); err != nil {
		return err
	}
	file, err := os.Open(target)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx, file}); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}
func download(ctx context.Context, opts options, name, target string, max int64, log func(string)) error {
	var last error
	for index, base := range opts.bases {
		if err := ctx.Err(); err != nil {
			return err
		}
		log(fmt.Sprintf("Downloading %s (source %d/%d)", name, index+1, len(opts.bases)))
		err := fetch(ctx, opts.http, base+name, target, max, func(received, total int64) {
			if total > 0 {
				log(fmt.Sprintf("Downloading %s: %d/%d bytes (%d%%)", name, received, total, received*100/total))
			} else {
				log(fmt.Sprintf("Downloading %s: %d bytes", name, received))
			}
		})
		if err == nil {
			return nil
		}
		last = err
		os.Remove(target)
	}
	return fmt.Errorf("download %s failed: %w", name, last)
}
func fetch(ctx context.Context, httpClient *http.Client, url, target string, max int64, progress func(int64, int64)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "Docklane-worker-installer")
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if response.ContentLength > max {
		return errors.New("download exceeds size limit")
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	var received int64
	last := time.Now()
	buffer := make([]byte, 128<<10)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var n int
		n, err = response.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			if received > max {
				return errors.New("download exceeds size limit")
			}
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
			if time.Since(last) >= time.Second {
				progress(received, response.ContentLength)
				last = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if response.ContentLength >= 0 && received != response.ContentLength {
		return io.ErrUnexpectedEOF
	}
	progress(received, response.ContentLength)
	return file.Close()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// Release transports may redirect to asset CDNs, but must never downgrade TLS.
func secureRedirect(request *http.Request, via []*http.Request) error {
	if request.URL.Scheme != "https" {
		return errors.New("refusing non-HTTPS worker download redirect")
	}
	if len(via) >= 10 {
		return errors.New("too many worker download redirects")
	}
	return nil
}
