// Package aiops implements automated root-cause-analysis correlation for
// the log aggregator: given detected metric anomalies, known deployments,
// and log-volume spikes, it proposes RootCauseHypothesis records
// explaining likely causes.
//
// This is a rule-and-statistics based correlator, NOT a trained machine
// learning model. Concretely:
//
//   - For each Anomaly, it looks for the nearest PRECEDING Deployment on
//     the same service within the correlation window, and computes a
//     confidence score that decays with the time distance between the
//     deploy and the anomaly (closer deploys are more likely causes).
//   - It looks for LogSpikes on the same service (or a small hard-coded
//     set of "dependency" relationships) within the window and adds them
//     as corroborating evidence, boosting confidence.
//   - Human-readable Evidence strings are generated describing exactly
//     which signals were correlated and why (e.g. "deploy v1.2.3 preceded
//     anomaly by 4m12s", "log spike of 812 messages on the same service
//     within the window").
//
// There is no learned weighting, no historical training data, and no
// probabilistic model beyond the explicit decay/scoring functions defined
// in correlate.go — everything here is deterministic given its inputs and
// easy to reason about, which is the intended trade-off versus a black-box
// ML root-cause model.
package aiops
