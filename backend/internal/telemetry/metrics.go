package telemetry

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var durationBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type httpKey struct {
	method, route, statusClass string
}

type cycleKey struct {
	role, outcome string
}

type histogram struct {
	count   uint64
	sum     float64
	buckets [len(durationBuckets)]uint64
}

func (h *histogram) observe(value float64) {
	if value < 0 {
		value = 0
	}
	h.count++
	h.sum += value
	for index, boundary := range durationBuckets {
		if value <= boundary {
			h.buckets[index]++
		}
	}
}

type cycleStats struct {
	cycles      uint64
	items       uint64
	lastSuccess int64
	duration    histogram
}

type queueStats struct {
	pending   uint64
	oldestAge float64
}

type providerProbeKey struct {
	outcome, category string
}

// Registry is a dependency-free, concurrency-safe Prometheus text collector.
// Every label is derived from a closed vocabulary or an http.ServeMux pattern;
// request paths, query strings, identities, errors, and remote URLs are never
// retained.
type Registry struct {
	mu                   sync.RWMutex
	service              string
	ready                float64
	http                 map[httpKey]*histogram
	cycles               map[cycleKey]*cycleStats
	queues               map[string]queueStats
	scannerHeadLag       uint64
	scannerGaps          map[string]uint64
	scannerReorgs        uint64
	providerProbes       map[providerProbeKey]uint64
	providerOpenCircuits uint64
	providerPeerGroups   uint64
}

func New(service string) *Registry {
	return &Registry{
		service:        normalizeService(service),
		http:           make(map[httpKey]*histogram),
		cycles:         make(map[cycleKey]*cycleStats),
		queues:         make(map[string]queueStats),
		scannerGaps:    make(map[string]uint64),
		providerProbes: make(map[providerProbeKey]uint64),
	}
}

// Handler adds a private /metrics endpoint and records the wrapped handler.
// Edge routing must not publish /metrics; the endpoint belongs on an internal
// service or health listener.
func (registry *Registry) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/metrics" {
			if request.Method != http.MethodGet {
				response.Header().Set("Allow", http.MethodGet)
				response.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			registry.ServeHTTP(response, request)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: response}
		observed := false
		defer func() {
			if recovered := recover(); recovered != nil {
				registry.observeHTTP(request, http.StatusInternalServerError, time.Since(started))
				panic(recovered)
			}
			if !observed {
				registry.observeHTTP(request, recorder.statusCode(), time.Since(started))
			}
		}()
		next.ServeHTTP(recorder, request)
		registry.observeHTTP(request, recorder.statusCode(), time.Since(started))
		observed = true
	})
}

func (registry *Registry) observeHTTP(request *http.Request, status int, elapsed time.Duration) {
	key := httpKey{method: normalizeMethod(request.Method), route: canonicalRoute(request.Pattern), statusClass: normalizeStatus(status)}
	registry.mu.Lock()
	metric := registry.http[key]
	if metric == nil {
		metric = &histogram{}
		registry.http[key] = metric
	}
	metric.observe(elapsed.Seconds())
	if key.route == "/readyz" {
		if status >= 200 && status < 300 {
			registry.ready = 1
		} else {
			registry.ready = 0
		}
	}
	registry.mu.Unlock()
}

func (registry *Registry) SetReady(ready bool) {
	registry.mu.Lock()
	registry.ready = 0
	if ready {
		registry.ready = 1
	}
	registry.mu.Unlock()
}

func (registry *Registry) ObserveCycle(role, outcome string, processed int, elapsed time.Duration) {
	key := cycleKey{role: normalizeRole(role), outcome: normalizeOutcome(outcome)}
	registry.mu.Lock()
	metric := registry.cycles[key]
	if metric == nil {
		metric = &cycleStats{}
		registry.cycles[key] = metric
	}
	metric.cycles++
	if processed > 0 {
		metric.items += uint64(processed)
	}
	metric.duration.observe(elapsed.Seconds())
	if key.outcome == "success" || key.outcome == "idle" {
		metric.lastSuccess = time.Now().UTC().Unix()
	}
	registry.mu.Unlock()
}

func (registry *Registry) SetQueue(queue string, pending int64, oldestAge time.Duration) {
	if pending < 0 {
		pending = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	registry.mu.Lock()
	registry.queues[normalizeQueue(queue)] = queueStats{pending: uint64(pending), oldestAge: oldestAge.Seconds()}
	registry.mu.Unlock()
}

func (registry *Registry) SetScannerHeadLag(blocks uint64) {
	registry.mu.Lock()
	registry.scannerHeadLag = blocks
	registry.mu.Unlock()
}

func (registry *Registry) IncScannerGap(reason string) {
	registry.mu.Lock()
	registry.scannerGaps[normalizeGap(reason)]++
	registry.mu.Unlock()
}

func (registry *Registry) IncScannerReorg() {
	registry.mu.Lock()
	registry.scannerReorgs++
	registry.mu.Unlock()
}

// ObserveProviderProbe records only closed-vocabulary SLO dimensions. Provider
// IDs, tenant IDs, URLs and raw errors are never accepted as labels.
func (registry *Registry) ObserveProviderProbe(success bool, category string) {
	outcome := "failure"
	if success {
		outcome = "success"
		category = "none"
	}
	key := providerProbeKey{outcome: outcome, category: normalizeProviderError(category)}
	registry.mu.Lock()
	registry.providerProbes[key]++
	registry.mu.Unlock()
}

func (registry *Registry) SetProviderHealth(openCircuits, admissiblePeerGroups int64) {
	if openCircuits < 0 {
		openCircuits = 0
	}
	if admissiblePeerGroups < 0 {
		admissiblePeerGroups = 0
	}
	registry.mu.Lock()
	registry.providerOpenCircuits = uint64(openCircuits)
	registry.providerPeerGroups = uint64(admissiblePeerGroups)
	registry.mu.Unlock()
}

func (registry *Registry) ServeHTTP(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	registry.mu.RLock()
	snapshot := registry.snapshot()
	registry.mu.RUnlock()
	_, _ = io.WriteString(response, snapshot)
}

func (registry *Registry) snapshot() string {
	var output strings.Builder
	service := escapeLabel(registry.service)
	output.WriteString("# HELP merchant_runtime_ready Whether the runtime most recently passed readiness.\n")
	output.WriteString("# TYPE merchant_runtime_ready gauge\n")
	fmt.Fprintf(&output, "merchant_runtime_ready{service=\"%s\"} %s\n", service, formatFloat(registry.ready))

	output.WriteString("# HELP merchant_http_requests_total HTTP requests by bounded method, router pattern, and status class.\n")
	output.WriteString("# TYPE merchant_http_requests_total counter\n")
	output.WriteString("# HELP merchant_http_request_duration_seconds HTTP request duration by bounded method, router pattern, and status class.\n")
	output.WriteString("# TYPE merchant_http_request_duration_seconds histogram\n")
	httpKeys := make([]httpKey, 0, len(registry.http))
	for key := range registry.http {
		httpKeys = append(httpKeys, key)
	}
	sort.Slice(httpKeys, func(i, j int) bool {
		left, right := httpKeys[i], httpKeys[j]
		return left.method+"\x00"+left.route+"\x00"+left.statusClass < right.method+"\x00"+right.route+"\x00"+right.statusClass
	})
	for _, key := range httpKeys {
		metric := registry.http[key]
		labels := fmt.Sprintf("service=\"%s\",method=\"%s\",route=\"%s\",status_class=\"%s\"", service, escapeLabel(key.method), escapeLabel(key.route), escapeLabel(key.statusClass))
		fmt.Fprintf(&output, "merchant_http_requests_total{%s} %d\n", labels, metric.count)
		writeHistogram(&output, "merchant_http_request_duration_seconds", labels, *metric)
	}

	output.WriteString("# HELP merchant_worker_cycles_total Worker cycles by fixed role and outcome.\n")
	output.WriteString("# TYPE merchant_worker_cycles_total counter\n")
	output.WriteString("# HELP merchant_worker_items_total Items handled by worker cycles.\n")
	output.WriteString("# TYPE merchant_worker_items_total counter\n")
	output.WriteString("# HELP merchant_worker_last_success_timestamp_seconds Last successful worker cycle as Unix time.\n")
	output.WriteString("# TYPE merchant_worker_last_success_timestamp_seconds gauge\n")
	output.WriteString("# HELP merchant_worker_cycle_duration_seconds Worker cycle duration by fixed role and outcome.\n")
	output.WriteString("# TYPE merchant_worker_cycle_duration_seconds histogram\n")
	cycleKeys := make([]cycleKey, 0, len(registry.cycles))
	for key := range registry.cycles {
		cycleKeys = append(cycleKeys, key)
	}
	sort.Slice(cycleKeys, func(i, j int) bool {
		return cycleKeys[i].role+"\x00"+cycleKeys[i].outcome < cycleKeys[j].role+"\x00"+cycleKeys[j].outcome
	})
	for _, key := range cycleKeys {
		metric := registry.cycles[key]
		labels := fmt.Sprintf("service=\"%s\",role=\"%s\",outcome=\"%s\"", service, key.role, key.outcome)
		fmt.Fprintf(&output, "merchant_worker_cycles_total{%s} %d\n", labels, metric.cycles)
		fmt.Fprintf(&output, "merchant_worker_items_total{%s} %d\n", labels, metric.items)
		if metric.lastSuccess > 0 {
			fmt.Fprintf(&output, "merchant_worker_last_success_timestamp_seconds{%s} %d\n", labels, metric.lastSuccess)
		}
		writeHistogram(&output, "merchant_worker_cycle_duration_seconds", labels, metric.duration)
	}

	output.WriteString("# HELP merchant_queue_pending Pending items in a fixed runtime queue.\n")
	output.WriteString("# TYPE merchant_queue_pending gauge\n")
	output.WriteString("# HELP merchant_queue_oldest_age_seconds Age of the oldest item in a fixed runtime queue.\n")
	output.WriteString("# TYPE merchant_queue_oldest_age_seconds gauge\n")
	queueNames := make([]string, 0, len(registry.queues))
	for name := range registry.queues {
		queueNames = append(queueNames, name)
	}
	sort.Strings(queueNames)
	for _, name := range queueNames {
		metric := registry.queues[name]
		labels := fmt.Sprintf("service=\"%s\",queue=\"%s\"", service, name)
		fmt.Fprintf(&output, "merchant_queue_pending{%s} %d\n", labels, metric.pending)
		fmt.Fprintf(&output, "merchant_queue_oldest_age_seconds{%s} %s\n", labels, formatFloat(metric.oldestAge))
	}

	output.WriteString("# HELP merchant_scanner_head_lag_blocks Difference between quorum-safe head and committed cursor.\n")
	output.WriteString("# TYPE merchant_scanner_head_lag_blocks gauge\n")
	fmt.Fprintf(&output, "merchant_scanner_head_lag_blocks{service=\"%s\"} %d\n", service, registry.scannerHeadLag)
	output.WriteString("# HELP merchant_scanner_gaps_total Durable scanner gaps by bounded reason.\n")
	output.WriteString("# TYPE merchant_scanner_gaps_total counter\n")
	gapNames := make([]string, 0, len(registry.scannerGaps))
	for reason := range registry.scannerGaps {
		gapNames = append(gapNames, reason)
	}
	sort.Strings(gapNames)
	for _, reason := range gapNames {
		fmt.Fprintf(&output, "merchant_scanner_gaps_total{service=\"%s\",reason=\"%s\"} %d\n", service, reason, registry.scannerGaps[reason])
	}
	output.WriteString("# HELP merchant_scanner_reorgs_total Canonical reorgs compensated by the scanner.\n")
	output.WriteString("# TYPE merchant_scanner_reorgs_total counter\n")
	fmt.Fprintf(&output, "merchant_scanner_reorgs_total{service=\"%s\"} %d\n", service, registry.scannerReorgs)

	output.WriteString("# HELP merchant_provider_probes_total Provider probes by bounded outcome and error category.\n")
	output.WriteString("# TYPE merchant_provider_probes_total counter\n")
	probeKeys := make([]providerProbeKey, 0, len(registry.providerProbes))
	for key := range registry.providerProbes {
		probeKeys = append(probeKeys, key)
	}
	sort.Slice(probeKeys, func(i, j int) bool {
		return probeKeys[i].outcome+"\x00"+probeKeys[i].category < probeKeys[j].outcome+"\x00"+probeKeys[j].category
	})
	for _, key := range probeKeys {
		fmt.Fprintf(&output, "merchant_provider_probes_total{service=\"%s\",outcome=\"%s\",error_category=\"%s\"} %d\n", service, key.outcome, key.category, registry.providerProbes[key])
	}
	output.WriteString("# HELP merchant_provider_circuits_open Open current provider operation circuits.\n")
	output.WriteString("# TYPE merchant_provider_circuits_open gauge\n")
	fmt.Fprintf(&output, "merchant_provider_circuits_open{service=\"%s\"} %d\n", service, registry.providerOpenCircuits)
	output.WriteString("# HELP merchant_provider_admissible_peer_groups Current independent provider peer groups admitted by health policy.\n")
	output.WriteString("# TYPE merchant_provider_admissible_peer_groups gauge\n")
	fmt.Fprintf(&output, "merchant_provider_admissible_peer_groups{service=\"%s\"} %d\n", service, registry.providerPeerGroups)
	return output.String()
}

func writeHistogram(output *strings.Builder, name, labels string, metric histogram) {
	for index, boundary := range durationBuckets {
		fmt.Fprintf(output, "%s_bucket{%s,le=\"%s\"} %d\n", name, labels, formatFloat(boundary), metric.buckets[index])
	}
	fmt.Fprintf(output, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labels, metric.count)
	fmt.Fprintf(output, "%s_sum{%s} %s\n", name, labels, formatFloat(metric.sum))
	fmt.Fprintf(output, "%s_count{%s} %d\n", name, labels, metric.count)
}

func normalizeService(value string) string {
	if !validToken(value, 48, true) {
		return "unknown"
	}
	return value
}

func normalizeMethod(value string) string {
	switch value {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return value
	default:
		return "OTHER"
	}
}

func normalizeStatus(status int) string {
	if status >= 100 && status <= 599 {
		return strconv.Itoa(status/100) + "xx"
	}
	return "other"
}

func canonicalRoute(pattern string) string {
	if _, route, found := strings.Cut(pattern, " "); found {
		pattern = route
	}
	if pattern == "" {
		return "_unmatched"
	}
	if len(pattern) > 160 || pattern[0] != '/' {
		return "_other"
	}
	var output strings.Builder
	for index := 0; index < len(pattern); {
		if pattern[index] == '{' {
			end := strings.IndexByte(pattern[index:], '}')
			if end < 1 {
				return "_other"
			}
			output.WriteString("{param}")
			index += end + 1
			continue
		}
		character := pattern[index]
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || strings.ContainsRune("/-_.*", rune(character))) {
			return "_other"
		}
		output.WriteByte(character)
		index++
	}
	return output.String()
}

func normalizeRole(value string) string {
	switch value {
	case "settlements", "matching", "callbacks", "outbox", "resolutions", "proofs", "plans", "scanner", "rates", "reconciliation", "financial", "scheduler", "provider_health":
		return value
	default:
		return "other"
	}
}

func normalizeProviderError(value string) string {
	switch value {
	case "none", "timeout", "dns", "tls", "connect", "rate_limited", "auth_rejected", "upstream_4xx", "upstream_5xx", "invalid_response", "chain_mismatch", "genesis_mismatch", "stale_head", "divergent_response", "policy_denied":
		return value
	default:
		return "policy_denied"
	}
}

func normalizeOutcome(value string) string {
	switch value {
	case "success", "failure", "partial", "idle":
		return value
	default:
		return "failure"
	}
}

func normalizeQueue(value string) string {
	switch value {
	case "settlement", "matching", "callback", "outbox", "resolution", "proof", "reconciliation", "financial", "rates":
		return value
	default:
		return "other"
	}
}

func normalizeGap(value string) string {
	switch value {
	case "provider_error", "cross_chain_event", "non_contiguous_range":
		return value
	default:
		return "other"
	}
}

func validToken(value string, maximum int, allowHyphen bool) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || (allowHyphen && character == '-') {
			continue
		}
		return false
	}
	return true
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

func (recorder *statusRecorder) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		recorder.ResponseWriter.WriteHeader(status)
		return
	}
	if recorder.status != 0 {
		return
	}
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(value []byte) (int, error) {
	if recorder.status == 0 {
		recorder.WriteHeader(http.StatusOK)
	}
	return recorder.ResponseWriter.Write(value)
}

func (recorder *statusRecorder) statusCode() int {
	if recorder.status == 0 {
		return http.StatusOK
	}
	return recorder.status
}

func (recorder *statusRecorder) Flush() {
	if recorder.status == 0 {
		recorder.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(recorder.ResponseWriter).Flush()
}

func (recorder *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(recorder.ResponseWriter).Hijack()
}

func (recorder *statusRecorder) Push(target string, options *http.PushOptions) error {
	pusher, ok := recorder.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (recorder *statusRecorder) ReadFrom(source io.Reader) (int64, error) {
	if recorder.status == 0 {
		recorder.WriteHeader(http.StatusOK)
	}
	if reader, ok := recorder.ResponseWriter.(io.ReaderFrom); ok {
		return reader.ReadFrom(source)
	}
	return io.Copy(recorder.ResponseWriter, source)
}
