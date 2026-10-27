package pipeql

// LogRecord is a single log event. Time is a unix timestamp in seconds.
// Fields carries arbitrary structured fields beyond the well-known ones
// (Host, Service, Level, Message).
type LogRecord struct {
	Time    int64
	Host    string
	Service string
	Level   string
	Message string
	Fields  map[string]any
}

// LogSource is implemented by whatever storage backend pipeql queries
// against. filters are field=value equality matchers (from the `search`
// stage); keywords are free-text terms that must all appear in the
// record's message. start/end are unix seconds and are inclusive.
type LogSource interface {
	Search(filters map[string]string, keywords []string, start, end int64) ([]LogRecord, error)
}
