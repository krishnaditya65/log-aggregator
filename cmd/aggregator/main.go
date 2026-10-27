package main

import (
	"log"
	"os"
	"strconv"
	"time"

	"custom-log-aggregator/internal/alerts"
	"custom-log-aggregator/internal/api"
	"custom-log-aggregator/internal/platform"
	"custom-log-aggregator/internal/processor"
	"custom-log-aggregator/internal/queue"
	"custom-log-aggregator/internal/storage"
)

func main() {
	dataDir := env("CLA_DATA_DIR", "./data")
	addr := env("CLA_HTTP_ADDR", ":8080")
	segmentSize := int64(1 << 20)

	q, err := queue.NewFileQueue(dataDir+"/queue", segmentSize)
	if err != nil {
		log.Fatalf("queue: %v", err)
	}

	store, err := storage.NewDiskStore(dataDir + "/store")
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	alertsSvc, err := alerts.NewService(dataDir + "/alerts.json")
	if err != nil {
		log.Fatalf("alerts: %v", err)
	}

	if err := q.Replay(func(env queue.Envelope) error {
		if env.Log != nil {
			return store.AppendLog(*env.Log)
		}
		if env.Metric != nil {
			if err := store.AppendMetric(*env.Metric); err != nil {
				return err
			}
			for _, event := range alertsSvc.Evaluate(*env.Metric) {
				log.Printf("alert replay: %+v", event)
			}
		}
		return nil
	}); err != nil {
		log.Printf("queue replay: %v", err)
	}

	proc := processor.New(q, store, alertsSvc)
	go proc.Run()

	// v2 advanced stack: OTLP ingestion, ingestbuffer, tsdb/logsdb,
	// metricsql/pipeql, alerting v2, RBAC, tracing, AIOps, vector search,
	// continuous profiling, cold storage + lifecycle. Runs side by side
	// with the v1 stack above; both share the same HTTP port.
	rbacEnabled := envBool("CLA_RBAC_ENABLED", false)
	rbacSecret := []byte(env("CLA_RBAC_SECRET", "dev-only-insecure-secret-change-me"))
	if rbacEnabled && env("CLA_RBAC_SECRET", "") == "" {
		log.Fatal("CLA_RBAC_ENABLED=true requires CLA_RBAC_SECRET to be set")
	}

	plat, err := platform.New(platform.Config{
		DataDir:      dataDir,
		RBACSecret:   rbacSecret,
		HotRetention: envDuration("CLA_HOT_RETENTION", 7*24*time.Hour),
		S3Endpoint:   env("CLA_S3_ENDPOINT", ""),
		S3Bucket:     env("CLA_S3_BUCKET", ""),
		S3AccessKey:  env("CLA_S3_ACCESS_KEY", ""),
		S3SecretKey:  env("CLA_S3_SECRET_KEY", ""),
		S3Region:     env("CLA_S3_REGION", "us-east-1"),
	})
	if err != nil {
		log.Fatalf("platform: %v", err)
	}
	if err := plat.Start(); err != nil {
		log.Fatalf("platform start: %v", err)
	}
	defer plat.Close()

	srv := api.NewServer(api.Config{
		Addr:            addr,
		Queue:           q,
		Store:           store,
		Alerts:          alertsSvc,
		DashboardOrigin: env("CLA_DASHBOARD_ORIGIN", "*"),
		Platform:        plat,
		RBACEnabled:     rbacEnabled,
		// Standalone rule-based alerting service (see cmd/alerting-service).
		AlertingServiceURL: env("ALERTING_SERVICE_URL", "http://localhost:8083"),
	})
	log.Printf("custom log aggregator listening on %s (v1+v2 API; OTLP ingestion now runs standalone in cmd/ingest-gateway)", addr)
	log.Fatal(srv.ListenAndServe())
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
