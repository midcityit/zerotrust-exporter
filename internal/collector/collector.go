package collector

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/vinistoisr/zerotrust-exporter/internal/appmetrics"
	"github.com/vinistoisr/zerotrust-exporter/internal/config"
	"github.com/vinistoisr/zerotrust-exporter/internal/devices"
	"github.com/vinistoisr/zerotrust-exporter/internal/dex"
	"github.com/vinistoisr/zerotrust-exporter/internal/tunnels"
	"github.com/vinistoisr/zerotrust-exporter/internal/users"
)

// Register metrics handler
func RegisterHandler() {
	http.HandleFunc("/metrics", MetricsHandler)
}

// StartServer starts the HTTP server
func StartServer(addr string) {
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// recoverCollector guards a collection goroutine so a panic in one collector
// (e.g. a nil dereference while handling a Cloudflare API 5xx) is contained:
// it logs, increments the API-error counter, and marks the exporter down for
// this scrape instead of crashing the whole process (MCIT-344 — the exporter
// previously crash-looped on intermittent Cloudflare 504s). For the devices
// goroutine it also closes the device-metrics channel on panic (recovered
// from a possible double-close) so the waiting users goroutine can't deadlock.
func recoverCollector(name string, deviceChan *chan map[string]devices.DeviceStatus) {
	if r := recover(); r != nil {
		log.Printf("recovered panic in %s collector: %v", name, r)
		appmetrics.IncApiErrorsCounter()
		appmetrics.SetUpMetric(0)
		if deviceChan != nil {
			// Close so the users goroutine's receive returns instead of blocking.
			// Guard against the channel already being closed.
			defer func() { _ = recover() }()
			close(*deviceChan)
		}
	}
}

// metricsHandler handles the /metrics endpoint
func MetricsHandler(w http.ResponseWriter, req *http.Request) {
	// Start timer for scrape duration
	startTime := time.Now()
	// create a channel between device metrics and user metrics
	deviceMetricsChan := make(chan map[string]devices.DeviceStatus)

	// Create a wait group to wait for all goroutines to complete
	var wg sync.WaitGroup
	wg.Add(4)

	// GO Collect device metrics
	go func() {
		defer wg.Done()
		defer recoverCollector("devices", &deviceMetricsChan)
		if config.EnableDevices {
			log.Println("Collecting device metrics...")
			deviceMetrics := devices.CollectDeviceMetrics()
			deviceMetricsChan <- deviceMetrics
			close(deviceMetricsChan)
		} else {
			close(deviceMetricsChan)
		}
	}()

	// GO Collect user metrics
	go func() {
		defer wg.Done()
		defer recoverCollector("users", nil)
		if config.EnableUsers {
			log.Println("Waiting for device metrics...")
			deviceMetrics, ok := <-deviceMetricsChan
			if ok {
				users.CollectUserMetrics(deviceMetrics)
			} else {
				log.Println("Failed to read device metrics from channel.")
			}
		}
	}()

	// GO Collect tunnel metrics
	go func() {
		defer wg.Done()
		defer recoverCollector("tunnels", nil)
		if config.EnableTunnels {
			log.Println("Collecting tunnel metrics...")
			tunnels.CollectTunnelMetrics()
		}
	}()

	// Go Collect dex metrics
	go func() {
		defer wg.Done()
		defer recoverCollector("dex", nil)
		if config.EnableDex {
			log.Println("Collecting dex metrics...")
			dex.CollectDexMetrics(context.TODO(), config.AccountID)
		}
	}()

	// Wait for all metrics collection to complete
	log.Println("Waiting for all metrics collection to complete...")
	wg.Wait()
	log.Println("All metrics collection completed.")

	// Update scrape duration metric
	appmetrics.ScrapeDuration.UpdateDuration(startTime)
	// Write metrics to the response
	metrics.WritePrometheus(w, true)

	// Print debug information if enabled
	if config.Debug {
		log.Printf("Scrape completed in %v", time.Since(startTime))
		log.Printf("API calls made: %d", appmetrics.ApiCallCounter.Get())
		log.Printf("API errors encountered: %d", appmetrics.ApiErrorsCounter.Get())
	}
}
