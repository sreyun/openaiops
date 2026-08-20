package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleMetricsForecastMultiSeries(t *testing.T) {
	srv, _ := newTestServer(t)
	now := time.Now().Unix()
	step := int64(60)
	mk := func(base float64, drift float64) [][2]float64 {
		pts := make([][2]float64, 0, 40)
		for i := 0; i < 40; i++ {
			pts = append(pts, [2]float64{
				float64(now - int64(40-i)*step),
				base + float64(i)*drift,
			})
		}
		return pts
	}
	body := map[string]any{
		"series": []map[string]any{
			{"name": "cpu_percent", "points": mk(20, 0.4)},
			{"name": "mem_percent", "points": mk(40, 0.2)},
			{"name": "net_recv", "points": mk(1e6, 1e3)},
		},
		"horizon_sec": 20 * step,
		"step":        step,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	srv.handleMetricsForecast(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res struct {
		OK     bool `json:"ok"`
		Series []struct {
			Name   string       `json:"name"`
			Kind   string       `json:"kind"`
			Points [][2]float64 `json:"points"`
		} `json:"series"`
		Meta forecastMeta `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected ok meta=%+v", res.Meta)
	}
	var histN, fcN int
	for _, s := range res.Series {
		switch s.Kind {
		case "history":
			histN++
		case "forecast":
			fcN++
			if len(s.Points) < 2 {
				t.Fatalf("forecast %q too short", s.Name)
			}
			// Future points should extend past now
			lastTS := int64(s.Points[len(s.Points)-1][0])
			if lastTS <= now {
				t.Fatalf("forecast %q lastTS=%d not after now=%d", s.Name, lastTS, now)
			}
		}
	}
	if histN < 3 || fcN < 3 {
		t.Fatalf("want ≥3 history+forecast pairs, got hist=%d fc=%d total=%d", histN, fcN, len(res.Series))
	}
	if res.Meta.NowTS <= 0 {
		t.Fatalf("missing now_ts in meta: %+v", res.Meta)
	}
}

func TestHandleMetricsForecastCapsSeries(t *testing.T) {
	srv, _ := newTestServer(t)
	now := time.Now().Unix()
	step := int64(30)
	series := make([]map[string]any, 0, metricsForecastMaxSeries+4)
	for n := 0; n < metricsForecastMaxSeries+4; n++ {
		pts := make([][2]float64, 0, 20)
		for i := 0; i < 20; i++ {
			pts = append(pts, [2]float64{float64(now - int64(20-i)*step), float64(10 + n + i)})
		}
		series = append(series, map[string]any{"name": fmt.Sprintf("s%d", n), "points": pts})
	}
	raw, _ := json.Marshal(map[string]any{"series": series, "step": step})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	srv.handleMetricsForecast(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var res struct {
		Series []struct {
			Kind string `json:"kind"`
		} `json:"series"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	hist := 0
	for _, s := range res.Series {
		if s.Kind == "history" {
			hist++
		}
	}
	if hist > metricsForecastMaxSeries {
		t.Fatalf("history series not capped: %d > %d", hist, metricsForecastMaxSeries)
	}
}

func TestHandleMetricsForecastDeterministic(t *testing.T) {
	srv, _ := newTestServer(t)
	now := int64(1_700_000_000)
	step := int64(60)
	pts := make([][2]float64, 0, 40)
	for i := 0; i < 40; i++ {
		pts = append(pts, [2]float64{float64(now - int64(40-i)*step), 20 + float64(i)*0.3})
	}
	body := map[string]any{
		"series":      []map[string]any{{"name": "cpu_percent", "points": pts}},
		"horizon_sec": 20 * step,
		"step":        step,
	}
	raw, _ := json.Marshal(body)
	call := func() [][2]float64 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader(raw))
		rr := httptest.NewRecorder()
		srv.handleMetricsForecast(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var res struct {
			Series []struct {
				Kind   string       `json:"kind"`
				Points [][2]float64 `json:"points"`
			} `json:"series"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, s := range res.Series {
			if s.Kind == "forecast" {
				return s.Points
			}
		}
		t.Fatal("no forecast series")
		return nil
	}
	a, b := call(), call()
	if len(a) != len(b) {
		t.Fatalf("length mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i][0] != b[i][0] || a[i][1] != b[i][1] {
			t.Fatalf("non-deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestHandleMetricsForecastRejectsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader([]byte(`{"series":[]}`)))
	rr := httptest.NewRecorder()
	srv.handleMetricsForecast(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestHandleMetricsForecastStaggeredEnds(t *testing.T) {
	srv, _ := newTestServer(t)
	now := time.Now().Unix()
	step := int64(60)
	mk := func(n int, endSkew int64, base float64) [][2]float64 {
		pts := make([][2]float64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]float64{
				float64(now - endSkew - int64(n-i)*step),
				base + float64(i)*0.2,
			})
		}
		return pts
	}
	// Dense CPU ends at `now`; sparse TCP ends 10 minutes earlier.
	body := map[string]any{
		"series": []map[string]any{
			{"name": "cpu_percent", "points": mk(40, 0, 20)},
			{"name": "conn_tcp", "points": mk(30, 10*step, 800)},
		},
		"horizon_sec": 20 * step,
		"step":        step,
		"now_ts":      now,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	srv.handleMetricsForecast(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res struct {
		OK     bool `json:"ok"`
		Series []struct {
			Name   string       `json:"name"`
			Kind   string       `json:"kind"`
			Points [][2]float64 `json:"points"`
		} `json:"series"`
		Meta forecastMeta `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected ok meta=%+v", res.Meta)
	}
	if res.Meta.NowTS < now-step {
		t.Fatalf("now_ts should align near global end, got %d want ~%d", res.Meta.NowTS, now)
	}
	for _, s := range res.Series {
		if s.Kind != "forecast" {
			continue
		}
		lastTS := int64(s.Points[len(s.Points)-1][0])
		if lastTS <= res.Meta.NowTS {
			t.Fatalf("forecast %q lastTS=%d must extend past now_ts=%d", s.Name, lastTS, res.Meta.NowTS)
		}
		// First forecast bridge should be at/near shared now
		bridge := int64(s.Points[0][0])
		if bridge < res.Meta.NowTS-step || bridge > res.Meta.NowTS+step {
			t.Fatalf("forecast %q bridge=%d not near now_ts=%d", s.Name, bridge, res.Meta.NowTS)
		}
	}
}

func TestHoldForwardTo(t *testing.T) {
	pts := [][2]float64{{100, 1}, {160, 2}, {220, 3}}
	got := holdForwardTo(pts, 400)
	if len(got) != 4 || got[3][0] != 400 || got[3][1] != 3 {
		t.Fatalf("holdForwardTo=%v", got)
	}
	same := holdForwardTo(pts, 220)
	if len(same) != 3 {
		t.Fatalf("no-op holdForwardTo changed length: %v", same)
	}
}

func TestHandleMetricsForecastObjectPoints(t *testing.T) {
	srv, _ := newTestServer(t)
	now := time.Now().Unix()
	step := int64(60)
	pts := make([]map[string]any, 0, 24)
	for i := 0; i < 24; i++ {
		pts = append(pts, map[string]any{
			"t": float64(now - int64(24-i)*step),
			"v": 30 + float64(i)*0.5,
		})
	}
	body := map[string]any{
		"series":      []map[string]any{{"name": "cpu_percent", "points": pts}},
		"horizon_sec": 12 * step,
		"step":        step,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/forecast", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	srv.handleMetricsForecast(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var res struct {
		OK     bool `json:"ok"`
		Series []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected ok for object-form points, body=%s", rr.Body.String())
	}
	var fcN int
	for _, s := range res.Series {
		if s.Kind == "forecast" {
			fcN++
			if !strings.Contains(s.Name, "预测") && !strings.HasPrefix(s.Name, "cpu_percent") {
				t.Fatalf("unexpected forecast name %q", s.Name)
			}
		}
	}
	if fcN < 1 {
		t.Fatalf("want forecast series, got %+v", res.Series)
	}
}
