package observability

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	ErrInvalidMetric = errors.New("observability: invalid metric")
)

type MetricKind string

const (
	Counter          MetricKind = "counter"
	Gauge            MetricKind = "gauge"
	Histogram        MetricKind = "histogram"
	maxMetricSamples            = 4096
)

type MetricDefinition struct {
	Name    string
	Help    string
	Kind    MetricKind
	Buckets []float64
}

type Label struct {
	Name  string
	Value string
}

type metricSample struct {
	definition MetricDefinition
	labels     []Label
	value      float64
	counts     []uint64
	count      uint64
	sum        float64
}

// Metrics is an in-memory, low-cardinality metric registry. Labels are
// normalized and sorted, and callers should use only bounded dimensions such
// as component, operation, outcome, and classified error.
type Metrics struct {
	mu          sync.RWMutex
	definitions map[string]MetricDefinition
	samples     map[string]*metricSample
}

func NewMetrics() *Metrics {
	return &Metrics{definitions: make(map[string]MetricDefinition), samples: make(map[string]*metricSample)}
}

func (m *Metrics) Register(def MetricDefinition) error {
	if m == nil || !validMetricName(def.Name) || (def.Kind != Counter && def.Kind != Gauge && def.Kind != Histogram) {
		return ErrInvalidMetric
	}
	if def.Help == "" {
		def.Help = def.Name
	}
	if len(def.Help) > 256 || strings.ContainsAny(def.Help, "\r\n") {
		return ErrInvalidMetric
	}
	if def.Kind != Histogram && len(def.Buckets) != 0 {
		return ErrInvalidMetric
	}
	if def.Kind == Histogram {
		if len(def.Buckets) == 0 {
			def.Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10}
		}
		previous := -1.0
		for _, bucket := range def.Buckets {
			if bucket < 0 || bucket <= previous || math.IsNaN(bucket) || math.IsInf(bucket, 0) {
				return ErrInvalidMetric
			}
			previous = bucket
		}
		def.Buckets = append([]float64(nil), def.Buckets...)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.definitions == nil {
		m.definitions = make(map[string]MetricDefinition)
	}
	if existing, ok := m.definitions[def.Name]; ok {
		if existing.Kind != def.Kind || existing.Help != def.Help || !equalBuckets(existing.Buckets, def.Buckets) {
			return fmt.Errorf("%w: metric %q definition conflict", ErrInvalidMetric, def.Name)
		}
		return nil
	}
	m.definitions[def.Name] = def
	return nil
}

func (m *Metrics) ensure(name string, kind MetricKind) bool {
	if m == nil || !validMetricName(name) {
		return false
	}
	if m.definitions == nil {
		m.definitions = make(map[string]MetricDefinition)
	}
	if m.samples == nil {
		m.samples = make(map[string]*metricSample)
	}
	if _, ok := m.definitions[name]; !ok {
		m.definitions[name] = MetricDefinition{Name: name, Help: name, Kind: kind}
	}
	return m.definitions[name].Kind == kind
}

func (m *Metrics) Inc(name string, labels ...Label) { m.Add(name, 1, labels...) }

func (m *Metrics) Add(name string, value float64, labels ...Label) {
	if m == nil || value != value || value < 0 {
		return
	}
	canonical, normalized, ok := canonicalLabels(labels)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ensure(name, Counter) {
		return
	}
	sample := m.sampleLocked(name, canonical, normalized)
	if sample == nil {
		return
	}
	sample.value += value
}

func (m *Metrics) Set(name string, value float64, labels ...Label) {
	if m == nil || value != value {
		return
	}
	canonical, normalized, ok := canonicalLabels(labels)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ensure(name, Gauge) {
		return
	}
	sample := m.sampleLocked(name, canonical, normalized)
	if sample == nil {
		return
	}
	sample.value = value
}

func (m *Metrics) Observe(name string, duration time.Duration, labels ...Label) {
	if m == nil || duration < 0 {
		return
	}
	canonical, normalized, ok := canonicalLabels(labels)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ensure(name, Histogram) {
		return
	}
	definition := m.definitions[name]
	sample := m.sampleLocked(name, canonical, normalized)
	if sample == nil {
		return
	}
	if len(sample.counts) == 0 {
		sample.counts = make([]uint64, len(definition.Buckets)+1)
	}
	seconds := duration.Seconds()
	for index, bucket := range definition.Buckets {
		if seconds <= bucket {
			sample.counts[index]++
		}
	}
	sample.counts[len(definition.Buckets)]++
	sample.count++
	sample.sum += seconds
}

func (m *Metrics) sampleLocked(name, canonical string, labels []Label) *metricSample {
	key := strconv.Itoa(len(name)) + ":" + name + "|" + canonical
	if sample, ok := m.samples[key]; ok {
		return sample
	}
	if len(m.samples) >= maxMetricSamples {
		return nil
	}
	definition := m.definitions[name]
	sample := &metricSample{definition: definition, labels: labels}
	m.samples[key] = sample
	return sample
}

// Record maps a classified event to a fixed-dimension counter. Request and
// resource identifiers are deliberately excluded to prevent cardinality and
// secret leakage through the metrics endpoint.
func (m *Metrics) Record(event Event) {
	labels := make([]Label, 0, 4)
	if component := safeField(event.Component); component != "" {
		labels = append(labels, Label{Name: "component", Value: component})
	}
	if operation := safeField(event.Operation); operation != "" {
		labels = append(labels, Label{Name: "operation", Value: operation})
	}
	if outcome := safeField(event.Outcome); outcome != "" {
		labels = append(labels, Label{Name: "outcome", Value: outcome})
	}
	if class := safeField(event.ErrorClass); class != "" {
		labels = append(labels, Label{Name: "error_class", Value: class})
	}
	m.Inc("chuzi_events_total", labels...)
	if event.Duration > 0 {
		m.Observe("chuzi_event_duration_seconds", event.Duration, labels...)
	}
}

func (m *Metrics) Handler() http.Handler { return http.HandlerFunc(m.ServeHTTP) }

func (m *Metrics) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(m.Prometheus()))
}

// Prometheus returns a deterministic exposition snapshot.
func (m *Metrics) Prometheus() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	definitions := make(map[string]MetricDefinition, len(m.definitions))
	for name, definition := range m.definitions {
		definitions[name] = definition
	}
	samples := make([]metricSample, 0, len(m.samples))
	for _, sample := range m.samples {
		copySample := *sample
		copySample.labels = append([]Label(nil), sample.labels...)
		copySample.counts = append([]uint64(nil), sample.counts...)
		samples = append(samples, copySample)
	}
	m.mu.RUnlock()
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].definition.Name == samples[j].definition.Name {
			return labelsString(samples[i].labels) < labelsString(samples[j].labels)
		}
		return samples[i].definition.Name < samples[j].definition.Name
	})
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		definition := definitions[name]
		builder.WriteString("# HELP ")
		builder.WriteString(name)
		builder.WriteByte(' ')
		builder.WriteString(strings.ReplaceAll(definition.Help, "\n", " "))
		builder.WriteByte('\n')
		builder.WriteString("# TYPE ")
		builder.WriteString(name)
		builder.WriteByte(' ')
		builder.WriteString(string(definition.Kind))
		builder.WriteByte('\n')
		for _, sample := range samples {
			if sample.definition.Name != name {
				continue
			}
			if definition.Kind == Histogram {
				for index, count := range sample.counts {
					labels := append([]Label(nil), sample.labels...)
					if index < len(definition.Buckets) {
						labels = append(labels, Label{Name: "le", Value: strconv.FormatFloat(definition.Buckets[index], 'g', -1, 64)})
					} else {
						labels = append(labels, Label{Name: "le", Value: "+Inf"})
					}
					writeSample(&builder, name+"_bucket", labels, float64(count))
				}
				writeSample(&builder, name+"_sum", sample.labels, sample.sum)
				writeSample(&builder, name+"_count", sample.labels, float64(sample.count))
			} else {
				writeSample(&builder, name, sample.labels, sample.value)
			}
		}
	}
	return builder.String()
}

func validMetricName(name string) bool {
	return validName(name, true)
}

func validLabelName(name string) bool {
	return validName(name, false)
}

func validName(name string, allowColon bool) bool {
	if strings.TrimSpace(name) != name || name == "" || len(name) > 128 {
		return false
	}
	for index, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && (!allowColon || char != ':') {
			return false
		}
		if index == 0 && (char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func canonicalLabels(labels []Label) (string, []Label, bool) {
	if len(labels) > 8 {
		return "", nil, false
	}
	copyLabels := append([]Label(nil), labels...)
	for index := range copyLabels {
		if strings.TrimSpace(copyLabels[index].Name) != copyLabels[index].Name || !validLabelName(copyLabels[index].Name) || copyLabels[index].Name == "le" || strings.TrimSpace(copyLabels[index].Value) != copyLabels[index].Value || copyLabels[index].Value == "" || len(copyLabels[index].Value) > 64 {
			return "", nil, false
		}
		for _, char := range copyLabels[index].Value {
			if unicode.IsControl(char) || char == '"' || char == '\\' || char == '\n' || char == '\r' {
				return "", nil, false
			}
		}
	}
	sort.Slice(copyLabels, func(i, j int) bool { return copyLabels[i].Name < copyLabels[j].Name })
	for index := 1; index < len(copyLabels); index++ {
		if copyLabels[index-1].Name == copyLabels[index].Name {
			return "", nil, false
		}
	}
	return labelsKey(copyLabels), copyLabels, true
}

func equalBuckets(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func labelsKey(labels []Label) string {
	var builder strings.Builder
	for _, label := range labels {
		builder.WriteString(strconv.Itoa(len(label.Name)))
		builder.WriteByte(':')
		builder.WriteString(label.Name)
		builder.WriteString(strconv.Itoa(len(label.Value)))
		builder.WriteByte(':')
		builder.WriteString(label.Value)
		builder.WriteByte(';')
	}
	return builder.String()
}

func labelsString(labels []Label) string {
	if len(labels) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteByte('{')
	for index, label := range labels {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(label.Name)
		builder.WriteByte('=')
		builder.WriteString(label.Value)
	}
	builder.WriteByte('}')
	return builder.String()
}

func writeSample(builder *strings.Builder, name string, labels []Label, value float64) {
	builder.WriteString(name)
	if len(labels) > 0 {
		builder.WriteByte('{')
		for index, label := range labels {
			if index > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(label.Name)
			builder.WriteString("=\"")
			builder.WriteString(strings.ReplaceAll(strings.ReplaceAll(label.Value, `\`, `\\`), `"`, `\"`))
			builder.WriteString("\"")
		}
		builder.WriteByte('}')
	}
	builder.WriteByte(' ')
	builder.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	builder.WriteByte('\n')
}
