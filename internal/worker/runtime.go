package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// RuntimeInfo is Code's `code.runtime/1` sidecar: the launch facts Code
// establishes before it forwards a byte of the engine's stdout, and the
// measurements it appends once the engine has exited.
//
// It is a file rather than a frame so that the native stream stays native. A
// startup metadata frame would be one Babel-specific object mixed into OMP's
// protocol, and every other host would have to know to skip it; a private
// file beside the pipe is read by exactly the process that asked for it.
type RuntimeInfo struct {
	Schema      string            `json:"schema"`
	Worker      Identity          `json:"worker"`
	Profile     ProfileRef        `json:"profile"`
	Privacy     Privacy           `json:"privacy"`
	Cost        Cost              `json:"cost"`
	Metadata    map[string]string `json:"metadata"`
	Containment *Containment      `json:"containment"`

	// Finished, ExitCode and Resources are present only in the report Code
	// rewrites after the engine exits. A wrapper killed before it could
	// write that report leaves the launch report in place, so Finished is
	// how a reader tells the two apart; the measurements are never claimed
	// from a report that does not carry them.
	Finished            bool       `json:"finished"`
	ExitCode            *int       `json:"exit_code"`
	Resources           *Resources `json:"resources"`
	ResourcesProvenance string     `json:"resources_provenance"`

	// Unknown lists top-level fields this build did not read.
	Unknown []string `json:"-"`
}

// readRuntimeInfo decodes the sidecar at path. A missing file is
// ErrRuntimeInfo: once the engine's ready frame has arrived Code has promised
// the file exists, so its absence means the process on the pipe is not Code
// or not the Code this build knows.
func readRuntimeInfo(path string) (*RuntimeInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: Code wrote no runtime-info before the engine became ready", ErrRuntimeInfo)
		}
		return nil, fmt.Errorf("%w: %v", ErrRuntimeInfo, err)
	}
	return decodeRuntimeInfo(data)
}

// decodeRuntimeInfo validates the document's schema and shape, and records the
// fields it did not read.
func decodeRuntimeInfo(data []byte) (*RuntimeInfo, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRuntimeInfo, err)
	}
	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRuntimeInfo, err)
	}
	if info.Schema != RuntimeInfoSchema {
		return nil, fmt.Errorf("%w: schema %q, this build reads %q", ErrRuntimeInfo, info.Schema, RuntimeInfoSchema)
	}
	if info.Profile.ID == "" {
		return nil, fmt.Errorf("%w: no profile", ErrRuntimeInfo)
	}
	if info.Worker.Name == "" {
		return nil, fmt.Errorf("%w: no worker identity", ErrRuntimeInfo)
	}
	if info.Resources != nil && info.ResourcesProvenance != "" {
		info.Resources.Provenance = info.ResourcesProvenance
	}
	for _, name := range knownRuntimeFields {
		delete(fields, name)
	}
	for name := range fields {
		info.Unknown = append(info.Unknown, name)
	}
	sort.Strings(info.Unknown)
	return &info, nil
}

// knownRuntimeFields are the top-level keys this build reads.
var knownRuntimeFields = []string{
	"schema", "worker", "profile", "privacy", "cost", "metadata", "containment",
	"finished", "exit_code", "resources", "resources_provenance",
}

// configurationOf is the describe document as a Configuration.
func (info *RuntimeInfo) configurationOf() *Configuration {
	return &Configuration{
		Profile:  info.Profile,
		Privacy:  info.Privacy,
		Cost:     info.Cost,
		Metadata: info.Metadata,
		Worker:   info.Worker,
		Unknown:  info.Unknown,
	}
}
