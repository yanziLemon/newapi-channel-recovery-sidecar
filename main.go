package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	baseURL        string
	token          string
	userID         string
	interval       time.Duration
	timeout        time.Duration
	concurrency    int
	successCount   int
	recoveryGroups []string
}

type apiClient struct {
	baseURL, token, userID string
	http                   *http.Client
}
type apiResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}
type channel struct {
	ID      int    `json:"id"`
	Status  int    `json:"status"`
	AutoBan int    `json:"auto_ban"`
	Group   string `json:"group"`
}
type channelPage struct {
	Items []channel `json:"items"`
	Total int       `json:"total"`
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func loadConfig() (config, error) {
	interval, err := envInt("CHECK_INTERVAL_SECONDS", 60)
	if err != nil {
		return config{}, err
	}
	timeout, err := envInt("TEST_TIMEOUT_SECONDS", 60)
	if err != nil {
		return config{}, err
	}
	concurrency, err := envInt("CHECK_CONCURRENCY", 1)
	if err != nil {
		return config{}, err
	}
	successCount, err := envInt("SUCCESS_COUNT", 1)
	if err != nil {
		return config{}, err
	}
	if interval < 1 || timeout < 1 || concurrency < 1 || successCount < 1 {
		return config{}, errors.New("interval, timeout, concurrency and success count must be positive")
	}
	token, userID := env("NEWAPI_ACCESS_TOKEN", ""), env("NEWAPI_USER_ID", "")
	if token == "" || userID == "" {
		return config{}, errors.New("NEWAPI_ACCESS_TOKEN and NEWAPI_USER_ID are required")
	}
	var groups []string
	for _, group := range strings.Split(os.Getenv("RECOVERY_GROUPS"), ",") {
		if group = strings.TrimSpace(group); group != "" {
			groups = append(groups, group)
		}
	}
	return config{baseURL: strings.TrimRight(env("NEWAPI_URL", "http://new-api:3000"), "/"), token: token, userID: userID, interval: time.Duration(interval) * time.Second, timeout: time.Duration(timeout) * time.Second, concurrency: concurrency, successCount: successCount, recoveryGroups: groups}, nil
}

func (client *apiClient) request(ctx context.Context, method, path string, body, output any) error {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("New-Api-User", client.userID)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var envelope apiResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("NewAPI returned HTTP %d: %s", response.StatusCode, envelope.Message)
	}
	if !envelope.Success {
		return errors.New(envelope.Message)
	}
	if output != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}

func (client *apiClient) channels(ctx context.Context, groups []string) ([]channel, error) {
	if len(groups) == 0 {
		groups = []string{""}
	}
	seen := make(map[int]bool)
	var result []channel
	for _, group := range groups {
		for page := 1; ; page++ {
			params := url.Values{"status": {"0"}, "p": {strconv.Itoa(page)}, "page_size": {"100"}}
			if group != "" {
				params.Set("group", group)
			}
			var data channelPage
			if err := client.request(ctx, http.MethodGet, "/api/channel/?"+params.Encode(), nil, &data); err != nil {
				return nil, err
			}
			for _, item := range data.Items {
				if item.Status == 3 && item.AutoBan == 1 && !seen[item.ID] {
					seen[item.ID] = true
					result = append(result, item)
				}
			}
			if len(data.Items) == 0 || page*100 >= data.Total {
				break
			}
		}
	}
	return result, nil
}

func (client *apiClient) eligible(ctx context.Context, id int) bool {
	var item channel
	return client.request(ctx, http.MethodGet, fmt.Sprintf("/api/channel/%d", id), nil, &item) == nil && item.Status == 3 && item.AutoBan == 1
}
func (client *apiClient) test(ctx context.Context, id int) error {
	return client.request(ctx, http.MethodGet, fmt.Sprintf("/api/channel/test/%d", id), nil, nil)
}
func (client *apiClient) enable(ctx context.Context, id int) error {
	return client.request(ctx, http.MethodPost, fmt.Sprintf("/api/channel/%d/status", id), map[string]int{"status": 1}, nil)
}

func runOnce(ctx context.Context, client *apiClient, cfg config, successes map[int]int, lock *sync.Mutex) {
	channels, err := client.channels(ctx, cfg.recoveryGroups)
	if err != nil {
		log.Printf("channel list failed: %v", err)
		return
	}
	log.Printf("found %d auto-disabled channel(s)", len(channels))
	if len(channels) == 0 {
		return
	}
	workers := cfg.concurrency
	if workers > len(channels) {
		workers = len(channels)
	}
	jobs := make(chan channel)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for item := range jobs {
				checkOne(ctx, client, cfg, item, successes, lock)
			}
		}()
	}
	for _, item := range channels {
		jobs <- item
	}
	close(jobs)
	wait.Wait()
}

func checkOne(ctx context.Context, client *apiClient, cfg config, item channel, successes map[int]int, lock *sync.Mutex) {
	if err := client.test(ctx, item.ID); err != nil {
		lock.Lock()
		delete(successes, item.ID)
		lock.Unlock()
		log.Printf("channel %d remains auto-disabled: %v", item.ID, err)
		return
	}
	lock.Lock()
	successes[item.ID]++
	count := successes[item.ID]
	lock.Unlock()
	if count < cfg.successCount {
		log.Printf("channel %d passed %d/%d tests", item.ID, count, cfg.successCount)
		return
	}
	if !client.eligible(ctx, item.ID) {
		lock.Lock()
		delete(successes, item.ID)
		lock.Unlock()
		log.Printf("channel %d changed while testing; do not enable", item.ID)
		return
	}
	if err := client.enable(ctx, item.ID); err != nil {
		log.Printf("channel %d passed but status update failed: %v", item.ID, err)
		return
	}
	lock.Lock()
	delete(successes, item.ID)
	lock.Unlock()
	log.Printf("channel %d enabled after successful test", item.ID)
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	client := &apiClient{baseURL: cfg.baseURL, token: cfg.token, userID: cfg.userID, http: &http.Client{Timeout: cfg.timeout}}
	successes := make(map[int]int)
	var lock sync.Mutex
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		runOnce(context.Background(), client, cfg, successes, &lock)
		<-ticker.C
	}
}
