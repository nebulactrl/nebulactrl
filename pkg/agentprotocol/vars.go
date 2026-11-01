package agentprotocol

// Build-time variables and source scanning.

// SecretBuildPrefix prefixes the keys of build-time variables in
// Command.Secrets: both plain build args and secret build variables travel as
// "build:<KEY>". The deploy payload lists which is which and carries no values.
const SecretBuildPrefix = "build:"

// ReplicaIndexEnv and ReplicaCountEnv are injected by the agent into every
// container (container-scoped platform variables).
const (
	ReplicaIndexEnv = "NEBULA_REPLICA_INDEX"
	ReplicaCountEnv = "NEBULA_REPLICA_COUNT"
)

// TypeSourceScan is the agent → control-plane frame carrying the keys of
// .env.example-style files found in a checked-out git source.
const TypeSourceScan = "source_scan"

// EnvVarHint is one KEY=value entry of a template env file. Values are
// example text from the repository, never secrets of ours.
type EnvVarHint struct {
	Key     string `json:"key"`
	Value   string `json:"value,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// EnvFileScan is one scanned template file.
type EnvFileScan struct {
	Path string       `json:"path"`
	Vars []EnvVarHint `json:"vars"`
}

// SourceScan reports the template env files and the project config files of a
// checked-out commit.
type SourceScan struct {
	ServiceID    string        `json:"serviceId"`
	DeploymentID string        `json:"deploymentId"`
	Commit       string        `json:"commit"`
	Files        []EnvFileScan `json:"files"`
	// ConfigPaths are the project config file paths the agent looked for
	// (nebula.toml at the repository root and in the build context); a path
	// missing from ConfigFiles does not exist at Commit. Empty: not looked at.
	ConfigPaths []string `json:"configPaths,omitempty"`
	// ConfigFiles holds the content of the ConfigPaths found (regular files of
	// at most ScanMaxConfigFileBytes).
	ConfigFiles map[string][]byte `json:"configFiles,omitempty"`
}

// Scan limits.
const (
	ScanMaxFileBytes = 64 * 1024
	ScanMaxVars      = 500
)
