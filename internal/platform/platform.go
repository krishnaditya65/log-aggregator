// Package platform is the integration layer: it constructs every storage
// engine, ingestion path, and analysis subsystem built under internal/*
// and wires them into one running system, matching the architecture:
//
//	Ingestion (HTTP/OTLP) -> Buffer (ingestbuffer) -> Stream Processor
//	(consumer.go) -> Storage (tsdb + logsdb) -> Query API (metricsql +
//	pipeql) -> Alerting/AIOps -> Cold storage + lifecycle.
//
// The v1 stack (internal/storage, internal/queue, internal/alerts) that
// shipped in the original scaffold is left completely untouched and keeps
// running side by side for backward compatibility with the existing
// dashboard; Platform is the "v2" advanced stack layered next to it.
package platform

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"custom-log-aggregator/internal/aiops"
	"custom-log-aggregator/internal/alerting"
	"custom-log-aggregator/internal/coldstore"
	"custom-log-aggregator/internal/ingestbuffer"
	"custom-log-aggregator/internal/lifecycle"
	"custom-log-aggregator/internal/logsdb"
	"custom-log-aggregator/internal/profiling"
	"custom-log-aggregator/internal/rbac"
	"custom-log-aggregator/internal/tracing"
	"custom-log-aggregator/internal/tsdb"
	"custom-log-aggregator/internal/vectorsearch"
)

// Config controls how Platform builds its subsystems. Zero-value fields
// fall back to sane defaults in New.
type Config struct {
	DataDir string

	// Buffer (Kafka-like ingestbuffer) tuning.
	BufferPartitions int
	BufferShardSize  int
	BufferReplicas   int // number of simulated-AZ replica dirs; quorum = majority

	// RBAC. If Secret is empty, RBAC enforcement is disabled (dev mode) -
	// all v2 routes are open, matching the default docker-compose flow.
	RBACSecret []byte

	// Retention applied to tsdb/logsdb's own segment expiry (which drives
	// their OnExpireSegment -> cold-storage-archival hook). Lifecycle's
	// own hot/warm/expire policy operates one layer further down, over
	// already-archived cold storage objects, so the two don't fight over
	// the same live segment files.
	HotRetention time.Duration

	// S3-compatible cold storage. If Endpoint is empty, archival uses a
	// local-filesystem Blobstore under DataDir/cold instead.
	S3Endpoint, S3Bucket, S3AccessKey, S3SecretKey, S3Region string

	Hostname string
}

func (c *Config) setDefaults() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.BufferPartitions <= 0 {
		c.BufferPartitions = 8
	}
	if c.BufferShardSize <= 0 {
		c.BufferShardSize = 2
	}
	if c.BufferReplicas <= 0 {
		c.BufferReplicas = 3
	}
	if c.HotRetention <= 0 {
		c.HotRetention = 7 * 24 * time.Hour
	}
	if c.Hostname == "" {
		h, err := os.Hostname()
		if err != nil {
			h = "unknown-host"
		}
		c.Hostname = h
	}
}

// Platform holds every constructed subsystem plus a background stream
// processor draining the ingest buffer into storage.
type Platform struct {
	cfg Config

	TSDB   *tsdb.Store
	LogsDB *logsdb.Store

	Buffer *ingestbuffer.Buffer

	RBAC       *rbac.Store
	RBACSecret []byte

	// Alerting is intentionally a "bare" engine here (no grouper/router/
	// silencer/deploy-correlator wired in) - it exists solely so
	// ObserveMetric/AddAnomalyDetector (the anomaly-scoring feed into
	// aiopsFeed, driven synchronously from consumer.go's processMetric)
	// keeps working. Rule-based alerting (rules/grouping/routing/
	// silencing/deployment-correlation) has moved to the standalone
	// cmd/alerting-service binary; see internal/alertingsvc.
	Alerting *alerting.Engine

	Blobstore coldstore.Blobstore
	Archive   *coldstore.Archiver
	Lifecycle *lifecycle.Manager

	Traces    *tracing.SpanStore
	Sampler   *tracing.TailSampler
	Exemplars *tracing.ExemplarStore

	Vector *vectorsearch.SemanticIndex

	Profiler *profiling.Profiler

	aiopsFeed    *aiopsFeed
	traceArchive *traceArchive

	compactor *tsdb.Compactor

	cancel context.CancelFunc
}

// New constructs every subsystem. It does not yet start network listeners
// or background loops - call Start for that, so callers can wire the HTTP
// API against a fully-built Platform first.
func New(cfg Config) (*Platform, error) {
	cfg.setDefaults()

	p := &Platform{cfg: cfg, RBACSecret: cfg.RBACSecret}

	var err error
	p.TSDB, err = tsdb.NewStore(tsdb.Options{
		Dir:                  filepath.Join(cfg.DataDir, "tsdb"),
		CardinalityThreshold: 2000,
		Retention:            cfg.HotRetention,
		OnExpireSegment:      p.archiveRawSegment("tsdb"),
	})
	if err != nil {
		return nil, fmt.Errorf("platform: tsdb: %w", err)
	}

	p.LogsDB, err = logsdb.NewStore(logsdb.Config{
		Dir:       filepath.Join(cfg.DataDir, "logsdb"),
		Retention: cfg.HotRetention,
	})
	if err != nil {
		return nil, fmt.Errorf("platform: logsdb: %w", err)
	}
	p.LogsDB.OnExpireSegment(func(path string, minTime, maxTime int64) {
		p.archiveRawSegment("logsdb")(path, minTime, maxTime)
	})

	// Cold storage: S3-compatible if configured, else local filesystem.
	if cfg.S3Endpoint != "" {
		p.Blobstore = &coldstore.S3Blobstore{
			Endpoint:  cfg.S3Endpoint,
			Bucket:    cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Region:    cfg.S3Region,
		}
	} else {
		fsBlob, err := coldstore.NewFSBlobstore(filepath.Join(cfg.DataDir, "cold"))
		if err != nil {
			return nil, fmt.Errorf("platform: blobstore: %w", err)
		}
		p.Blobstore = fsBlob
	}
	p.Archive = &coldstore.Archiver{Store: p.Blobstore, Service: "aggregator", TimeField: "time"}
	p.Lifecycle = newColdLifecycle(p.Blobstore)

	// Ingest buffer (Kafka-like commit log) sitting between ingestion and
	// the stream processor.
	replicaDirs := make([]string, cfg.BufferReplicas)
	for i := range replicaDirs {
		replicaDirs[i] = filepath.Join(cfg.DataDir, "buffer", fmt.Sprintf("az-%d", i))
	}
	p.Buffer, err = ingestbuffer.New(ingestbuffer.Config{
		Dir:               filepath.Join(cfg.DataDir, "buffer", "primary"),
		Partitions:        cfg.BufferPartitions,
		ShardSize:         cfg.BufferShardSize,
		MaxPartitionDepth: 50000,
		Policy:            ingestbuffer.PolicyReject,
		ReplicaDirs:       replicaDirs,
		TenantRateLimit:   2000,
		TenantBurst:       4000,
	})
	if err != nil {
		return nil, fmt.Errorf("platform: ingestbuffer: %w", err)
	}

	p.RBAC = rbac.NewStore()
	bootstrapRBAC(p.RBAC)

	p.Traces = tracing.NewSpanStore(30 * time.Second)
	p.Sampler = tracing.NewTailSampler(2 * time.Second)
	p.Exemplars = tracing.NewExemplarStore()

	p.Vector = vectorsearch.NewSemanticIndex(
		vectorsearch.NewHashingEmbedder(128, nil),
		vectorsearch.Config{Dim: 128},
	)

	p.Profiler = profiling.NewProfiler("aggregator", cfg.Hostname, 50)

	p.aiopsFeed = newAIOpsFeed()

	// Bare engine: no grouper/router/silencer/correlator. Those live in
	// cmd/alerting-service now (see internal/alertingsvc). This instance
	// only ever sees ObserveMetric/AddAnomalyDetector calls, driven from
	// consumer.go's processMetric.
	p.Alerting = alerting.NewEngine(nil, nil, nil, nil)

	return p, nil
}

// Start launches the stream processor and the periodic background loops
// (compaction, alert evaluation, lifecycle). The OTLP receiver no longer
// runs in this process - it lives in the standalone cmd/ingest-gateway
// binary, which forwards to this process's /internal/{logs,metrics,traces}
// endpoints (see internal/ingestgateway and internal/api/v2.go).
func (p *Platform) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	p.compactor = tsdb.NewCompactor(p.TSDB)
	p.compactor.Start(ctx, 5*time.Minute)

	p.wireTraceArchive(ctx)
	p.startConsumer(ctx)
	// lifecycle.Manager.Start blocks its calling goroutine (unlike
	// tsdb.Compactor.Start, which backgrounds itself) - run it in its own
	// goroutine so Platform.Start returns promptly.
	go p.Lifecycle.Start(ctx, 10*time.Minute)

	return nil
}

// Close shuts down every subsystem in reverse dependency order.
func (p *Platform) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.compactor != nil {
		p.compactor.Stop()
	}
	if p.Buffer != nil {
		_ = p.Buffer.Close()
	}
	if p.TSDB != nil {
		_ = p.TSDB.Close()
	}
	if p.LogsDB != nil {
		_ = p.LogsDB.Close()
	}
	return nil
}

// archiveRawSegment returns an OnExpireSegment hook that copies an
// about-to-be-deleted, already-compressed segment file's raw bytes into
// cold storage before the owning Store deletes its local copy. This is
// the "Dynamic Retention" flow: segments age out of the hot tier, get
// archived, then removed - all coordinated by the owning Store so it never
// serves a Query() against a segment path that has vanished out from under
// it.
func (p *Platform) archiveRawSegment(engine string) func(path string, minTime, maxTime int64) {
	return func(path string, minTime, maxTime int64) {
		f, err := os.Open(path)
		if err != nil {
			log.Printf("platform: archive %s segment %s: open: %v", engine, path, err)
			return
		}
		defer f.Close()
		// minTime/maxTime arrive as unix nanoseconds from tsdb/logsdb;
		// the key encodes unix seconds since that is what
		// lifecycle.SegmentMeta (and RunOnce's age math) expects.
		key := fmt.Sprintf("%s/raw/%d-%d-%s", engine, minTime/1e9, maxTime/1e9, filepath.Base(path))
		if err := p.Blobstore.Put(context.Background(), key, f); err != nil {
			log.Printf("platform: archive %s segment %s: put: %v", engine, path, err)
			return
		}
		log.Printf("archived %s segment %s -> cold storage key %s", engine, filepath.Base(path), key)
	}
}

// aiopsFeed keeps a small bounded in-memory history of anomalies,
// deployments, and log spikes so /v2/aiops/rootcause has real recent data
// to correlate against, per internal/aiops.Correlate.
type aiopsFeed struct {
	mu          chan struct{} // 1-buffered mutex
	anomalies   []aiops.Anomaly
	deployments []aiops.Deployment
	spikes      []aiops.LogSpike
}

func newAIOpsFeed() *aiopsFeed {
	f := &aiopsFeed{mu: make(chan struct{}, 1)}
	f.mu <- struct{}{}
	return f
}

func (f *aiopsFeed) lock()   { <-f.mu }
func (f *aiopsFeed) unlock() { f.mu <- struct{}{} }

const aiopsFeedCap = 500

func (f *aiopsFeed) addAnomaly(a aiops.Anomaly) {
	f.lock()
	defer f.unlock()
	f.anomalies = appendCapped(f.anomalies, a, aiopsFeedCap)
}

func (f *aiopsFeed) addDeployment(d aiops.Deployment) {
	f.lock()
	defer f.unlock()
	f.deployments = appendCapped(f.deployments, d, aiopsFeedCap)
}

func (f *aiopsFeed) addSpike(s aiops.LogSpike) {
	f.lock()
	defer f.unlock()
	f.spikes = appendCapped(f.spikes, s, aiopsFeedCap)
}

func (f *aiopsFeed) snapshot() ([]aiops.Anomaly, []aiops.Deployment, []aiops.LogSpike) {
	f.lock()
	defer f.unlock()
	a := make([]aiops.Anomaly, len(f.anomalies))
	copy(a, f.anomalies)
	d := make([]aiops.Deployment, len(f.deployments))
	copy(d, f.deployments)
	s := make([]aiops.LogSpike, len(f.spikes))
	copy(s, f.spikes)
	return a, d, s
}

func appendCapped[T any](s []T, v T, cap int) []T {
	s = append(s, v)
	if len(s) > cap {
		s = s[len(s)-cap:]
	}
	return s
}

// AddDeploymentEvent records a deployment with the standalone AIOps
// correlator. Deploy-correlated root-cause hints for rule-based alerts are
// handled by cmd/alerting-service's own DeployCorrelator (see
// internal/alertingsvc); this is unrelated and stays local, since AIOps
// root-cause analysis is entirely an aggregator-side concern (p.aiopsFeed).
func (p *Platform) AddDeploymentEvent(service, version string, at time.Time) {
	p.aiopsFeed.addDeployment(aiops.Deployment{Service: service, Time: at.Unix(), Version: version})
}

// RootCauseAnalysis runs internal/aiops.Correlate against the recent
// anomaly/deployment/log-spike history this Platform has observed.
func (p *Platform) RootCauseAnalysis(window time.Duration) []aiops.RootCauseHypothesis {
	an, dep, sp := p.aiopsFeed.snapshot()
	return aiops.Correlate(an, dep, sp, window)
}
