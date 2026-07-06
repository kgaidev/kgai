package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"kgai/internal/store"
)

// cloudRemote speaks the same write-once segment protocol as the S3 backend, but
// through the kgai cloud broker: the broker authorizes the token, scopes all keys to
// the token's org/project, and mints presigned S3 URLs — segment bytes go straight to
// object storage, never through the service.
//
// Configuration:
//
//	remote URL        kgai://org/project   (informative; authorization is the token's)
//	KGAI_CLOUD_TOKEN  bearer token (or cloud_token in kg.config.json)
//	KGAI_CLOUD_URL    broker base URL (e.g. https://api.example.com or a local broker)
type cloudRemote struct {
	url string
}

func newCloudRemote(url string) (Remote, error) {
	return &cloudRemote{url: url}, nil
}

func (c *cloudRemote) Sync(s *store.Store) (SyncResult, error) {
	base := strings.TrimRight(firstNonEmpty(os.Getenv("KGAI_CLOUD_URL"), s.Config.CloudURL), "/")
	token := firstNonEmpty(os.Getenv("KGAI_CLOUD_TOKEN"), s.Config.CloudToken)
	res := SyncResult{Remote: c.url}
	if base == "" {
		res.Detail = "no cloud endpoint configured — set KGAI_CLOUD_URL (and KGAI_CLOUD_TOKEN)"
		return res, nil
	}
	if token == "" {
		res.Detail = "no cloud token — set KGAI_CLOUD_TOKEN or `kg init --token <token>`"
		return res, nil
	}
	or := &objectRemote{
		os:     &brokerStore{base: base, token: token, http: &http.Client{Timeout: 60 * time.Second}},
		prefix: "", // the broker scopes keys to the token's org/project
		name:   c.url,
	}
	return or.Sync(s)
}

// brokerStore implements ObjectStore against the broker API + presigned S3 URLs.
type brokerStore struct {
	base  string
	token string
	http  *http.Client
}

func (b *brokerStore) call(path string, body any, out any) error {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, b.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("broker %s: %s (%s)", path, resp.Status, e.Error)
	}
	return json.Unmarshal(data, out)
}

func (b *brokerStore) List(_ string) ([]string, error) {
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := b.call("/v1/segments", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

func (b *brokerStore) Get(key string) ([]byte, error) {
	var out struct {
		Gets map[string]string `json:"gets"`
	}
	if err := b.call("/v1/segments/urls", map[string]any{"gets": []string{key}}, &out); err != nil {
		return nil, err
	}
	url, ok := out.Gets[key]
	if !ok {
		return nil, fmt.Errorf("broker returned no URL for %s", key)
	}
	resp, err := b.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("segment download: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func (b *brokerStore) Put(key string, data []byte) error {
	var out struct {
		Put string `json:"put"`
	}
	if err := b.call("/v1/segments/urls", map[string]any{"put": key}, &out); err != nil {
		return err
	}
	if out.Put == "" {
		return fmt.Errorf("broker returned no upload URL for %s", key)
	}
	req, err := http.NewRequest(http.MethodPut, out.Put, bytes.NewReader(data))
	if err != nil {
		return err
	}
	// Signed into the URL by the broker — S3 refuses the write if the segment exists.
	req.Header.Set("If-None-Match", "*")
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("segment upload: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
