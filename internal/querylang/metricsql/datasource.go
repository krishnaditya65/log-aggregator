package metricsql

// Point is a single (timestamp, value) sample. T is a unix timestamp in
// seconds.
type Point struct {
	T int64
	V float64
}

// Series is a labeled time series: a set of label key/value pairs plus the
// samples that fell within the requested query window.
type Series struct {
	Labels map[string]string
	Points []Point
}

// DataSource is implemented by whatever storage backend metricsql queries
// against. matchers always includes "__name__" for the metric name plus
// any label matchers from the query. start/end are unix seconds and are
// inclusive.
type DataSource interface {
	QueryRange(matchers map[string]string, start, end int64) ([]Series, error)
}
