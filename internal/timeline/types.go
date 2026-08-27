// Package timeline defines the dynamic, non-causal temporal read model.
package timeline

import "time"

const DefaultLimit = 100

type Query struct {
	Environment, Service, Cursor string
	Since, Until                 time.Time
	Limit                        int
}

type Confidence struct {
	Level string `json:"level"`
	Basis string `json:"basis"`
}
type Subject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Source struct {
	Kind       string
	ObservedAt time.Time
}
type Deployment struct {
	Status               string     `json:"status"`
	Strategy             string     `json:"strategy"`
	ProvenanceStatus     string     `json:"provenance_status"`
	ProvenanceConfidence Confidence `json:"provenance_confidence"`
}
type Runtime struct {
	State            string `json:"state"`
	Health           string `json:"health"`
	RestartCount     int    `json:"restart_count"`
	ArtifactIdentity string `json:"artifact_identity"`
}
type Concurrency struct {
	Detected bool `json:"detected"`
	Count    int  `json:"count"`
}
type Event struct {
	ID, Kind, Environment, Service, RelationType string
	Time                                         time.Time
	Subject                                      Subject
	Source                                       Source
	Confidence                                   Confidence
	Deployment                                   *Deployment
	Runtime                                      *Runtime
	Concurrency                                  Concurrency
	Limitations                                  []string
	CausalityClaimed                             bool
}
type Result struct {
	Since, Until time.Time
	Environment  string
	Items        []Event
	NextCursor   string
}

func DeploymentKind(status string) string {
	switch status {
	case "pending":
		return "deployment_pending"
	case "running":
		return "deployment_running"
	case "succeeded":
		return "deployment_succeeded"
	case "failed":
		return "deployment_failed"
	case "cancelled":
		return "deployment_cancelled"
	case "rolled_back":
		return "rollback_declared"
	default:
		return "deployment_unknown"
	}
}

func Priority(kind string) int {
	if kind == "rollback_declared" {
		return 10
	}
	if kind == "runtime_observed" {
		return 30
	}
	return 20
}
