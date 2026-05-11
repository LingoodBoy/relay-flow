package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	host        string
	users       int
	spawnRate   float64
	timeout     time.Duration
	agentID     string
	prompt      string
	httpTimeout time.Duration
}

type sample struct {
	ms            float64
	size          int64
	failed        bool
	failureReason string
}

type metric struct {
	requestType string
	name        string
	mu          sync.Mutex
	samples     []sample
	currentRPS  float64
	currentFail float64
	lastCount   int
	lastFails   int
}

type metricSummary struct {
	requestType string
	name        string
	requests    int
	fails       int
	median      float64
	p95         float64
	p99         float64
	average     float64
	min         float64
	max         float64
	avgSize     float64
	currentRPS  float64
	currentFail float64
}

type failureEntry struct {
	requestType string
	name        string
	message     string
	count       int
}

type benchmark struct {
	cfg       config
	client    *http.Client
	post      *metric
	get       *metric
	sse       *metric
	started   atomic.Int64
	activeSSE atomic.Int64
	completed atomic.Int64
}

type createRunResponse struct {
	RunID string `json:"run_id"`
}

// main 解析命令行参数并启动完整的 POST + SSE 链路压测。
func main() {
	cfg := parseConfig()
	if cfg.host == "" {
		fmt.Fprintln(os.Stderr, "-host is required")
		os.Exit(2)
	}
	if cfg.users <= 0 {
		fmt.Fprintln(os.Stderr, "-users must be greater than 0")
		os.Exit(2)
	}
	if cfg.spawnRate <= 0 {
		fmt.Fprintln(os.Stderr, "-spawn-rate must be greater than 0")
		os.Exit(2)
	}

	bench := newBenchmark(cfg)
	if err := bench.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseConfig 读取压测参数，默认值对齐当前 Locust 脚本的请求体。
func parseConfig() config {
	var cfg config
	flag.StringVar(&cfg.host, "host", "http://127.0.0.1:8080", "RelayFlow Gateway base URL")
	flag.IntVar(&cfg.users, "users", 1, "total virtual users")
	flag.Float64Var(&cfg.spawnRate, "spawn-rate", 1, "users spawned per second")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Minute, "maximum benchmark duration")
	flag.StringVar(&cfg.agentID, "agent-id", "langgraph", "agent_id used in POST /v1/runs")
	flag.StringVar(&cfg.prompt, "prompt", "帮我查一下北京今天的天气", "prompt used in POST /v1/runs")
	flag.DurationVar(&cfg.httpTimeout, "http-timeout", 0, "HTTP client timeout, 0 means no global client timeout")
	flag.Parse()
	cfg.host = strings.TrimRight(cfg.host, "/")
	return cfg
}

// newBenchmark 创建压测运行器和三类 Locust 风格指标。
func newBenchmark(cfg config) *benchmark {
	return &benchmark{
		cfg:    cfg,
		client: newHTTPClient(cfg.httpTimeout),
		post:   newMetric("POST", "POST /v1/runs", cfg.users),
		get:    newMetric("GET", "GET /v1/runs/:run_id/events", cfg.users),
		sse:    newMetric("SSE", "events received", cfg.users),
	}
}

// newHTTPClient 创建适合大量长连接的 HTTP 客户端。
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          100000,
		MaxIdleConnsPerHost:   100000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// run 按指定速率启动虚拟用户，并在结束后输出汇总表。
func (b *benchmark) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.timeout)
	defer cancel()

	start := time.Now()
	fmt.Printf("Starting ssebench: host=%s users=%d spawn-rate=%.2f timeout=%s\n", b.cfg.host, b.cfg.users, b.cfg.spawnRate, b.cfg.timeout)

	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.spawnUsers(ctx, &wg)
		wg.Wait()
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			b.printProgress(time.Since(start))
			fmt.Println()
			b.printSummary()
			return nil
		case <-ticker.C:
			b.refreshRates(time.Second)
			b.printProgress(time.Since(start))
		case <-ctx.Done():
			<-done
			b.printProgress(time.Since(start))
			fmt.Println()
			b.printSummary()
			return fmt.Errorf("benchmark stopped: %w", ctx.Err())
		}
	}
}

// spawnUsers 以固定速率创建虚拟用户；每个用户只执行一次 POST + SSE。
func (b *benchmark) spawnUsers(ctx context.Context, wg *sync.WaitGroup) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	spawned := 0
	credit := 0.0
	for spawned < b.cfg.users {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			credit += b.cfg.spawnRate / 10
			batch := int(credit)
			if batch == 0 {
				continue
			}
			credit -= float64(batch)
			if remain := b.cfg.users - spawned; batch > remain {
				batch = remain
			}
			for i := 0; i < batch; i++ {
				spawned++
				b.started.Add(1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					b.runUser(ctx)
				}()
			}
		}
	}
}

// runUser 执行单个虚拟用户的完整链路，失败时记录到对应指标后结束。
func (b *benchmark) runUser(ctx context.Context) {
	runID, ok := b.submitRun(ctx)
	if !ok {
		return
	}
	if b.subscribeRunEvents(ctx, runID) {
		b.completed.Add(1)
	}
}

// submitRun 调用 POST /v1/runs，并返回创建出的 run_id。
func (b *benchmark) submitRun(ctx context.Context) (string, bool) {
	body, err := json.Marshal(map[string]any{
		"agent_id": b.cfg.agentID,
		"input": map[string]any{
			"prompt": b.cfg.prompt,
		},
	})
	if err != nil {
		b.post.record(0, 0, true, "marshal request body: "+shortError(err))
		return "", false
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.host+"/v1/runs", bytes.NewReader(body))
	if err != nil {
		b.post.record(durationMS(start), 0, true, "create request: "+shortError(err))
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		b.post.record(durationMS(start), 0, true, "ConnectionError("+shortError(err)+")")
		return "", false
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		b.post.record(durationMS(start), int64(len(respBody)), true, "read response body: "+shortError(readErr))
		return "", false
	}
	if resp.StatusCode != http.StatusAccepted {
		b.post.record(durationMS(start), int64(len(respBody)), true, unexpectedStatusReason(resp.StatusCode, respBody))
		return "", false
	}

	var parsed createRunResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		b.post.record(durationMS(start), int64(len(respBody)), true, "invalid create run response: "+shortError(err))
		return "", false
	}
	if parsed.RunID == "" {
		b.post.record(durationMS(start), int64(len(respBody)), true, "invalid create run response: missing run_id")
		return "", false
	}
	b.post.record(durationMS(start), int64(len(respBody)), false, "")
	return parsed.RunID, true
}

// subscribeRunEvents 建立 SSE 连接并等待 succeeded、failed 或 dead_letter 终态事件。
func (b *benchmark) subscribeRunEvents(ctx context.Context, runID string) bool {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.host+"/v1/runs/"+runID+"/events", nil)
	if err != nil {
		reason := "create request: " + shortError(err)
		b.get.record(durationMS(start), 0, true, reason)
		return false
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := b.client.Do(req)
	if err != nil {
		reason := "ConnectionError(" + shortError(err) + ")"
		b.get.record(durationMS(start), 0, true, reason)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		reason := unexpectedStatusReason(resp.StatusCode, respBody)
		b.get.record(durationMS(start), int64(len(respBody)), true, reason)
		return false
	}
	b.get.record(durationMS(start), 0, false, "")

	b.activeSSE.Add(1)
	defer b.activeSSE.Add(-1)

	eventCount, terminal, err := readSSEEvents(resp.Body)
	if err != nil {
		b.sse.record(durationMS(start), int64(eventCount), true, "read sse stream: "+shortError(err))
		return false
	}
	if !terminal {
		b.sse.record(durationMS(start), int64(eventCount), true, "sse ended without terminal event")
		return false
	}
	b.sse.record(durationMS(start), int64(eventCount), false, "")
	return true
}

// readSSEEvents 读取 SSE 流，直到遇到任务终态事件或连接结束。
func readSSEEvents(body io.Reader) (int, bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	eventType := "message"
	dataLines := make([]string, 0, 4)
	eventCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			if len(dataLines) > 0 {
				eventCount++
				if isTerminalEvent(eventType) {
					return eventCount, true, nil
				}
			}
			eventType = "message"
			dataLines = dataLines[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return eventCount, false, err
	}
	return eventCount, false, nil
}

// isTerminalEvent 判断 SSE 事件是否代表任务已经结束。
func isTerminalEvent(eventType string) bool {
	return eventType == "succeeded" || eventType == "failed" || eventType == "dead_letter"
}

// newMetric 创建单项指标并预分配样本空间。
func newMetric(requestType string, name string, capacity int) *metric {
	return &metric{
		requestType: requestType,
		name:        name,
		samples:     make([]sample, 0, capacity),
	}
}

// record 记录一次请求或 SSE 阶段结果，失败样本会额外保存聚合用的错误原因。
func (m *metric) record(ms float64, size int64, failed bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !failed {
		reason = ""
	}
	m.samples = append(m.samples, sample{ms: ms, size: size, failed: failed, failureReason: reason})
}

// refreshRate 根据最近一个统计窗口更新 Current RPS 和 Current Failures/s。
func (m *metric) refreshRate(window time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := len(m.samples)
	fails := 0
	for _, s := range m.samples {
		if s.failed {
			fails++
		}
	}
	seconds := window.Seconds()
	m.currentRPS = float64(count-m.lastCount) / seconds
	m.currentFail = float64(fails-m.lastFails) / seconds
	m.lastCount = count
	m.lastFails = fails
}

// summary 计算单项指标的请求数、失败数、分位数和平均响应大小。
func (m *metric) summary() metricSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	return summarizeSamples(m.requestType, m.name, m.samples, m.currentRPS, m.currentFail)
}

// summarizeSamples 对一组样本计算 Locust 风格汇总值。
func summarizeSamples(requestType string, name string, samples []sample, currentRPS float64, currentFail float64) metricSummary {
	result := metricSummary{requestType: requestType, name: name, requests: len(samples), currentRPS: currentRPS, currentFail: currentFail}
	if len(samples) == 0 {
		return result
	}

	durations := make([]float64, 0, len(samples))
	var totalMS float64
	var totalSize int64
	for _, s := range samples {
		if s.failed {
			result.fails++
		}
		durations = append(durations, s.ms)
		totalMS += s.ms
		totalSize += s.size
	}
	sort.Float64s(durations)

	result.min = durations[0]
	result.max = durations[len(durations)-1]
	result.median = percentile(durations, 0.50)
	result.p95 = percentile(durations, 0.95)
	result.p99 = percentile(durations, 0.99)
	result.average = totalMS / float64(len(samples))
	result.avgSize = float64(totalSize) / float64(len(samples))
	return result
}

// percentile 返回最接近 Locust 展示语义的分位数值。
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// refreshRates 刷新所有指标最近一个窗口内的吞吐。
func (b *benchmark) refreshRates(window time.Duration) {
	b.post.refreshRate(window)
	b.get.refreshRate(window)
	b.sse.refreshRate(window)
}

// printProgress 输出单行实时进度，便于观察是否已经挂满连接。
func (b *benchmark) printProgress(elapsed time.Duration) {
	line := fmt.Sprintf("elapsed=%s started=%d active_sse=%d completed=%d post=%d/%d get=%d/%d sse=%d/%d",
		elapsed.Truncate(time.Second),
		b.started.Load(),
		b.activeSSE.Load(),
		b.completed.Load(),
		b.post.count(),
		b.post.failCount(),
		b.get.count(),
		b.get.failCount(),
		b.sse.count(),
		b.sse.failCount(),
	)
	if top, ok := b.topFailure(); ok {
		line += fmt.Sprintf(" top_fail=%s %s: %s (%d)", top.requestType, top.name, top.message, top.count)
	}
	fmt.Printf("\r\033[2K%s", line)
}

// printSummary 输出 Locust 风格的最终统计表。
func (b *benchmark) printSummary() {
	b.refreshRates(time.Second)
	summaries := []metricSummary{
		b.get.summary(),
		b.post.summary(),
		b.sse.summary(),
		b.aggregatedSummary(),
	}

	fmt.Println("Type\tName\t# Requests\t# Fails\tMedian (ms)\t95%ile (ms)\t99%ile (ms)\tAverage (ms)\tMin (ms)\tMax (ms)\tAverage size (bytes)\tCurrent RPS\tCurrent Failures/s")
	for _, s := range summaries {
		fmt.Printf("%s\t%s\t%d\t%d\t%.0f\t%.0f\t%.0f\t%.2f\t%.0f\t%.0f\t%.2f\t%.1f\t%.1f\n",
			s.requestType,
			s.name,
			s.requests,
			s.fails,
			s.median,
			s.p95,
			s.p99,
			s.average,
			s.min,
			s.max,
			s.avgSize,
			s.currentRPS,
			s.currentFail,
		)
	}
	b.printFailureSummary()
}

// aggregatedSummary 汇总 POST、GET 和 SSE 三类样本，方便和 Locust Aggregated 行对照。
func (b *benchmark) aggregatedSummary() metricSummary {
	samples := make([]sample, 0, b.post.count()+b.get.count()+b.sse.count())
	samples = append(samples, b.post.copySamples()...)
	samples = append(samples, b.get.copySamples()...)
	samples = append(samples, b.sse.copySamples()...)
	return summarizeSamples("Aggregated", "", samples, b.post.currentRate()+b.get.currentRate()+b.sse.currentRate(), b.post.currentFailRate()+b.get.currentFailRate()+b.sse.currentFailRate())
}

// count 返回当前指标已经记录的样本数量。
func (m *metric) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.samples)
}

// failCount 返回当前指标已经记录的失败样本数量。
func (m *metric) failCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	fails := 0
	for _, s := range m.samples {
		if s.failed {
			fails++
		}
	}
	return fails
}

// failureEntries 按错误原因聚合当前指标的失败样本。
func (m *metric) failureEntries() []failureEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts := make(map[string]int)
	for _, s := range m.samples {
		if s.failed {
			counts[s.failureReason]++
		}
	}

	entries := make([]failureEntry, 0, len(counts))
	for reason, count := range counts {
		entries = append(entries, failureEntry{
			requestType: m.requestType,
			name:        m.name,
			message:     reason,
			count:       count,
		})
	}
	sortFailureEntries(entries)
	return entries
}

// topFailure 返回全局出现次数最多的失败原因，用于实时进度行。
func (b *benchmark) topFailure() (failureEntry, bool) {
	entries := b.allFailureEntries()
	if len(entries) == 0 {
		return failureEntry{}, false
	}
	return entries[0], true
}

// allFailureEntries 汇总 POST、GET 和 SSE 三类指标的错误原因。
func (b *benchmark) allFailureEntries() []failureEntry {
	entries := make([]failureEntry, 0)
	entries = append(entries, b.post.failureEntries()...)
	entries = append(entries, b.get.failureEntries()...)
	entries = append(entries, b.sse.failureEntries()...)
	sortFailureEntries(entries)
	return entries
}

// sortFailureEntries 按次数倒序排列错误原因，次数相同则按接口名称稳定排序。
func sortFailureEntries(entries []failureEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		if entries[i].requestType != entries[j].requestType {
			return entries[i].requestType < entries[j].requestType
		}
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return entries[i].message < entries[j].message
	})
}

// printFailureSummary 输出类似 Locust Failure 面板的错误原因聚合表。
func (b *benchmark) printFailureSummary() {
	entries := b.allFailureEntries()
	if len(entries) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Failures")
	fmt.Println("# Fails\tType\tName\tMessage")
	for _, entry := range entries {
		fmt.Printf("%d\t%s\t%s\t%s\n", entry.count, entry.requestType, entry.name, entry.message)
	}
}

// copySamples 返回指标样本副本，避免汇总时和写入 goroutine 共享底层切片。
func (m *metric) copySamples() []sample {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]sample, len(m.samples))
	copy(copied, m.samples)
	return copied
}

// currentRate 返回最近一个统计窗口内的请求速率。
func (m *metric) currentRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentRPS
}

// currentFailRate 返回最近一个统计窗口内的失败速率。
func (m *metric) currentFailRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentFail
}

// durationMS 返回从 start 到当前时间的毫秒数。
func durationMS(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

// shortError 截断错误信息，避免压测时单条错误把进度行刷得过长。
func shortError(err error) string {
	if err == nil {
		return ""
	}
	return truncate(strings.ReplaceAll(err.Error(), "\n", " "), 240)
}

// unexpectedStatusReason 把非预期 HTTP 状态码和短响应体整理成失败原因。
func unexpectedStatusReason(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("unexpected status %d", status)
	}
	return fmt.Sprintf("unexpected status %d: %s", status, truncate(strings.ReplaceAll(text, "\n", " "), 200))
}

// truncate 把长字符串压到固定长度，保留对排障最有用的前缀。
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
