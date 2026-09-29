// Package buildinfo contains the compile-time identity shared by every Setpoint binary.
package buildinfo

// Linker-injected values. Developer builds deliberately have no release identity.
var Version = "dev"
var SourceSHA = "unknown"

type Identity struct {
	Version   string `json:"version"`
	SourceSHA string `json:"source_sha"`
}

func Current() Identity           { return Identity{Version: Version, SourceSHA: SourceSHA} }
func (i Identity) String() string { return "version=" + i.Version + " source_sha=" + i.SourceSHA }
