package framework

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fortio.org/fortio/fhttp"
	"fortio.org/fortio/stats"
	"github.com/sirupsen/logrus"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// StatsCollector collects latency and throughput counters.
// The ReportDuration() method is safe for concurrent use by multiple goroutines.
type StatsCollector struct {
	name      string
	outputDir string

	mu              sync.Mutex
	samples         []time.Duration
	statusCounts    map[int]int64
	firstSampleTime time.Time
	lastSampleTime  time.Time
}

// ReportDuration adds a single time measurement.
func (p *StatsCollector) ReportDuration(d time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := time.Now()
	if len(p.samples) == 0 {
		p.firstSampleTime = n
	}
	p.lastSampleTime = n
	p.samples = append(p.samples, d)
	if p.statusCounts == nil {
		p.statusCounts = map[int]int64{}
	}
	p.statusCounts[errToHTTPStatusCode(err)]++
}

func errToHTTPStatusCode(err error) int {
	// crude translation from 'err' to HTTP status code.
	switch {
	case err == nil:
		return http.StatusOK
	case k8serrors.IsNotFound(err):
		return http.StatusNotFound
	case k8serrors.IsConflict(err):
		return http.StatusConflict
	case k8serrors.IsUnauthorized(err):
		return http.StatusUnauthorized
	case k8serrors.IsServiceUnavailable(err):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Report outputs performance report to log.
func (p *StatsCollector) Report() {
	if len(p.samples) == 0 {
		return
	}

	h := stats.NewHistogram(0, 1)
	for _, s := range p.samples {
		h.Record(s.Seconds())
	}

	var rr fhttp.HTTPRunnerResults
	rr.RunType = "HTTP"
	rr.Labels = "Agones " + p.name
	rr.StartTime = time.Now()
	rr.ActualDuration = p.lastSampleTime.Sub(p.firstSampleTime)
	rr.DurationHistogram = h.Export()
	rr.DurationHistogram.CalcPercentiles([]float64{50, 90, 95, 99, 99.9})
	rr.RetCodes = map[int]int64{}
	rr.ActualQPS = float64(len(p.samples)) / rr.ActualDuration.Seconds()

	logrus.
		WithField("avg", rr.DurationHistogram.Avg).
		WithField("count", rr.DurationHistogram.Count).
		WithField("min", rr.DurationHistogram.Min).
		WithField("max", rr.DurationHistogram.Max).
		WithField("p50", rr.DurationHistogram.CalcPercentile(50)).
		WithField("p90", rr.DurationHistogram.CalcPercentile(90)).
		WithField("p95", rr.DurationHistogram.CalcPercentile(95)).
		WithField("p99", rr.DurationHistogram.CalcPercentile(99)).
		WithField("p999", rr.DurationHistogram.CalcPercentile(99.9)).
		WithField("duration", p.lastSampleTime.Sub(p.firstSampleTime).Seconds()).
		Info(p.name)

	if p.outputDir != "" {
		os.MkdirAll(p.outputDir, 0755) //nolint:errcheck

		fname := filepath.Join(p.outputDir, p.name+"_"+rr.StartTime.UTC().Format("2006-01-02_1504")+".json")
		f, err := os.Create(fname)
		if err != nil {
			logrus.WithError(err).Error("unable to create performance log")
			return
		}
		defer f.Close() //nolint:errcheck

		e := json.NewEncoder(f)
		e.SetIndent("", "  ")
		e.Encode(rr) //nolint:errcheck
	}
}
