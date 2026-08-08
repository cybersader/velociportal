package main

const unknownBuildValue = "unknown"

var (
	buildVersion     = "dev"
	buildRevision    = unknownBuildValue
	buildSourceState = unknownBuildValue
)

type BuildInfo struct {
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	SourceState string `json:"source_state"`
}

func currentBuildInfo() BuildInfo {
	return BuildInfo{
		Version:     buildVersion,
		Revision:    buildRevision,
		SourceState: buildSourceState,
	}
}

func (info BuildInfo) SourceTraceable() bool {
	return info.Revision != "" && info.Revision != unknownBuildValue && info.SourceState == "clean"
}
