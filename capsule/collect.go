package capsule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime/metrics"
	"runtime/pprof"
)

type payload struct {
	name string
	data []byte
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.buf.Len())+int64(len(p)) > b.max {
		return 0, ErrBudgetExceeded
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

type metricValue struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Uint64  *uint64  `json:"uint64,omitempty"`
	Float64 *float64 `json:"float64,omitempty"`
}

type metricsDocument struct {
	Schema  string        `json:"schema"`
	Version int           `json:"version"`
	Values  []metricValue `json:"values"`
}

func (r *Recorder) collect(ctx context.Context, _ string) ([]payload, error) {
	remaining := r.cfg.Limits.TotalUncompressedBytes
	take := func(name string, data []byte) (payload, error) {
		if int64(len(data)) > remaining {
			return payload{}, fmt.Errorf("collect %s: %w", name, ErrBudgetExceeded)
		}
		remaining -= int64(len(data))
		return payload{name: name, data: data}, nil
	}

	traceLimit := min(r.cfg.Limits.TraceBytes, remaining)
	traceBuffer := &limitedBuffer{max: traceLimit}
	if _, err := r.fr.WriteTo(traceBuffer); err != nil {
		return nil, fmt.Errorf("capture runtime trace: %w", err)
	}
	tracePayload, err := take("trace.out", traceBuffer.Bytes())
	if err != nil {
		return nil, err
	}
	payloads := []payload{tracePayload}

	metricPayload, err := r.collectMetrics(min(r.cfg.Limits.PayloadBytes, remaining))
	if err != nil {
		return nil, err
	}
	metricsItem, err := take("metrics.json", metricPayload)
	if err != nil {
		return nil, err
	}
	payloads = append(payloads, metricsItem)

	for _, name := range r.cfg.Profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		profile := pprof.Lookup(name)
		if profile == nil {
			return nil, fmt.Errorf("runtime profile %q is unavailable", name)
		}
		buf := &limitedBuffer{max: min(r.cfg.Limits.PayloadBytes, remaining)}
		if err := profile.WriteTo(buf, 0); err != nil {
			return nil, fmt.Errorf("capture %s profile: %w", name, err)
		}
		profileName := "profiles/" + name + ".pb.gz"
		profilePayload, err := take(profileName, buf.Bytes())
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, profilePayload)
	}
	return payloads, nil
}

func (r *Recorder) collectMetrics(limit int64) ([]byte, error) {
	samples := make([]metrics.Sample, len(r.cfg.Metrics))
	for i, name := range r.cfg.Metrics {
		samples[i].Name = name
	}
	// Go 1.25's runtime/metrics.Read panics for an empty sample slice.
	if len(samples) > 0 {
		metrics.Read(samples)
	}
	doc := metricsDocument{Schema: metricsSchema, Version: metricsVersion, Values: make([]metricValue, 0, len(samples))}
	for _, sample := range samples {
		value := metricValue{Name: sample.Name}
		switch sample.Value.Kind() {
		case metrics.KindUint64:
			v := sample.Value.Uint64()
			value.Kind, value.Uint64 = "uint64", &v
		case metrics.KindFloat64:
			v := sample.Value.Float64()
			value.Kind, value.Float64 = "float64", &v
		default:
			return nil, fmt.Errorf("runtime metric %q became non-scalar", sample.Name)
		}
		doc.Values = append(doc.Values, value)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode runtime metrics: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("metrics payload: %w", ErrBudgetExceeded)
	}
	return data, nil
}
