package main

import "testing"

func TestBuildInfoSourceTraceable(t *testing.T) {
	tests := []struct {
		name string
		info BuildInfo
		want bool
	}{
		{name: "clean revision", info: BuildInfo{Version: "v1.0.0", Revision: "abc123", SourceState: "clean"}, want: true},
		{name: "dirty", info: BuildInfo{Version: "dev", Revision: "abc123", SourceState: "dirty"}, want: false},
		{name: "unknown revision", info: BuildInfo{Version: "dev", Revision: unknownBuildValue, SourceState: "clean"}, want: false},
		{name: "unknown state", info: BuildInfo{Version: "dev", Revision: "abc123", SourceState: unknownBuildValue}, want: false},
		{name: "blank revision", info: BuildInfo{Version: "dev", SourceState: "clean"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.info.SourceTraceable(); got != test.want {
				t.Fatalf("SourceTraceable() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCurrentBuildInfoUsesInjectedVariables(t *testing.T) {
	originalVersion, originalRevision, originalState := buildVersion, buildRevision, buildSourceState
	t.Cleanup(func() {
		buildVersion, buildRevision, buildSourceState = originalVersion, originalRevision, originalState
	})
	buildVersion = "v9.8.7"
	buildRevision = "revision-test"
	buildSourceState = "clean"

	got := currentBuildInfo()
	if got.Version != buildVersion || got.Revision != buildRevision || got.SourceState != buildSourceState {
		t.Fatalf("currentBuildInfo() = %#v", got)
	}
}
