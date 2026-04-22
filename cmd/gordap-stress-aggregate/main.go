// gordap-stress-aggregate combines JSON reports from multiple parallel
// gordap-stress runs into a single rolled-up view. Use this when one
// load generator can't saturate the target and you fan out to N
// machines / containers / cloud instances.
//
// Each generator writes its report to a file:
//
//	gordap-stress -url=$TARGET -c=200 -d=60s -json > /tmp/run-$HOSTNAME.json
//
// The aggregator reads all of them and prints the combined result:
//
//	gordap-stress-aggregate /tmp/run-*.json
//
// Latency percentiles are aggregated by averaging the per-run
// percentiles weighted by request count — this is approximate but
// stable enough for ranking and trend detection. For exact
// cross-machine percentiles you'd need to ship raw samples; that's a
// separate engineering project (HDR-histogram serialisation).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type report struct {
	Target           string                    `json:"target"`
	Concurrency      int                       `json:"concurrency"`
	DurationSeconds  float64                   `json:"duration_seconds"`
	SeedCorpus       int                       `json:"seed_corpus"`
	TotalRequests    int64                     `json:"total_requests"`
	ThroughputRPS    float64                   `json:"throughput_rps"`
	TransportErrors  int64                     `json:"transport_errors"`
	ValidationFails  int64                     `json:"validation_failures"`
	LatencyMs        map[string]float64        `json:"latency_ms"`
	Cache            map[string]int64          `json:"cache"`
	ByEndpoint       map[string]map[string]any `json:"by_endpoint"`
	StatusCodes      map[string]int64          `json:"status_codes"`
	Mismatches       []string                  `json:"mismatches,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gordap-stress-aggregate REPORT.json [REPORT.json ...]")
		os.Exit(2)
	}

	var reports []report
	for _, path := range os.Args[1:] {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
			os.Exit(1)
		}
		var r report
		if err := json.NewDecoder(f).Decode(&r); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "decode %s: %v\n", path, err)
			os.Exit(1)
		}
		f.Close()
		reports = append(reports, r)
	}

	combined := aggregate(reports)

	fmt.Printf("=== aggregate of %d runs ===\n", len(reports))
	fmt.Printf("Total requests:       %d\n", combined.TotalRequests)
	fmt.Printf("Combined throughput:  %.0f req/s (sum across runs)\n", combined.ThroughputRPS)
	fmt.Printf("Transport errors:     %d\n", combined.TransportErrors)
	fmt.Printf("Validation failures:  %d\n", combined.ValidationFails)

	fmt.Println("\nLatency (request-weighted average of per-run percentiles):")
	for _, p := range []string{"p50", "p90", "p95", "p99", "p999", "max"} {
		fmt.Printf("  %-5s %8.2f ms\n", p, combined.LatencyMs[p])
	}

	if h := combined.Cache["hits"]; h+combined.Cache["misses"] > 0 {
		total := h + combined.Cache["misses"]
		fmt.Printf("\nCache hit ratio: %.1f%% (%d / %d)\n",
			100*float64(h)/float64(total), h, total)
	}

	fmt.Println("\nBy endpoint (counts summed, percentiles averaged)")
	keys := make([]string, 0, len(combined.ByEndpoint))
	for k := range combined.ByEndpoint {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("  %-22s %12s %10s %10s %10s\n", "kind", "count", "p50_ms", "p95_ms", "p99_ms")
	for _, k := range keys {
		v := combined.ByEndpoint[k]
		fmt.Printf("  %-22s %12d %10.2f %10.2f %10.2f\n", k,
			toInt(v["count"]), toFloat(v["p50_ms"]), toFloat(v["p95_ms"]), toFloat(v["p99_ms"]))
	}

	fmt.Println("\nStatus codes")
	statusKeys := make([]string, 0, len(combined.StatusCodes))
	for k := range combined.StatusCodes {
		statusKeys = append(statusKeys, k)
	}
	sort.Strings(statusKeys)
	for _, k := range statusKeys {
		v := combined.StatusCodes[k]
		fmt.Printf("  %s: %12d (%.2f%%)\n", k, v, 100*float64(v)/float64(combined.TotalRequests))
	}

	if len(combined.Mismatches) > 0 {
		fmt.Println("\nFirst mismatches across all runs:")
		for i, m := range combined.Mismatches {
			if i >= 10 {
				break
			}
			fmt.Printf("  - %s\n", m)
		}
	}
}

// aggregate merges per-run reports. Counts are summed; latency
// percentiles are weighted by request count to approximate the global
// distribution. Imprecise but monotone — useful for ranking, trend
// lines, and "is the cluster degrading" alerts.
func aggregate(rs []report) report {
	out := report{
		LatencyMs:    map[string]float64{},
		Cache:        map[string]int64{},
		ByEndpoint:   map[string]map[string]any{},
		StatusCodes:  map[string]int64{},
	}
	if len(rs) == 0 {
		return out
	}

	for _, r := range rs {
		out.TotalRequests += r.TotalRequests
		out.ThroughputRPS += r.ThroughputRPS
		out.TransportErrors += r.TransportErrors
		out.ValidationFails += r.ValidationFails
		for k, v := range r.Cache {
			out.Cache[k] += v
		}
		for k, v := range r.StatusCodes {
			out.StatusCodes[k] += v
		}
		out.Mismatches = append(out.Mismatches, r.Mismatches...)
	}

	totalReq := float64(out.TotalRequests)
	if totalReq == 0 {
		return out
	}
	for _, p := range []string{"p50", "p90", "p95", "p99", "p999", "max"} {
		var sum float64
		for _, r := range rs {
			sum += r.LatencyMs[p] * float64(r.TotalRequests)
		}
		out.LatencyMs[p] = sum / totalReq
	}

	// Endpoint roll-up.
	for _, r := range rs {
		for k, v := range r.ByEndpoint {
			cur, ok := out.ByEndpoint[k]
			if !ok {
				cur = map[string]any{
					"count":    int64(0),
					"failures": int64(0),
					"p50_ms":   0.0,
					"p95_ms":   0.0,
					"p99_ms":   0.0,
				}
				out.ByEndpoint[k] = cur
			}
			c := toInt(v["count"])
			cur["count"] = toInt(cur["count"]) + c
			cur["failures"] = toInt(cur["failures"]) + toInt(v["failures"])
			// Weighted percentile aggregation (per-endpoint).
			cur["p50_ms"] = toFloat(cur["p50_ms"]) + toFloat(v["p50_ms"])*float64(c)
			cur["p95_ms"] = toFloat(cur["p95_ms"]) + toFloat(v["p95_ms"])*float64(c)
			cur["p99_ms"] = toFloat(cur["p99_ms"]) + toFloat(v["p99_ms"])*float64(c)
		}
	}
	for _, v := range out.ByEndpoint {
		c := toInt(v["count"])
		if c > 0 {
			v["p50_ms"] = toFloat(v["p50_ms"]) / float64(c)
			v["p95_ms"] = toFloat(v["p95_ms"]) / float64(c)
			v["p99_ms"] = toFloat(v["p99_ms"]) / float64(c)
		}
	}
	return out
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}
