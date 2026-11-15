package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// personalApplicationDigest consumes active evidence from the personal evaluator
// and the SAME complete snapshot. It is not an authorization/ownership check or
// a public tuple API. Callers must freshly evaluate their own current entry; no
// stored digest, hostname, owner, login or personal ID is an identity fallback.
// S2 prepares this pure helper only; no rendering or mutation uses it yet.
func personalApplicationDigest(binding serviceMetadataBinding, data *CacheData, evidence personalServiceEvidence) (string, bool) {
	if binding.ControlPlane != controlPlaneTailscale || !validStateNamespace(binding.Namespace) || data == nil || data.ControlPlane.Provider != binding.ControlPlane || evidence.node.ID == "" || evidence.port < 1 || evidence.port > 65535 || evidence.escapedPath == "" {
		return "", false
	}
	id, ok := data.PresentationDeviceIDsByNodeID[evidence.node.ID]
	if !ok || id.Provider != binding.ControlPlane || !validPresentationDeviceID(id.Kind, id.Value) {
		return "", false
	}
	fields := [8]string{binding.Namespace, string(id.Provider), "personal_application", string(id.Kind), id.Value, "https", strconv.Itoa(evidence.port), evidence.escapedPath}
	hash := sha256.New()
	_, _ = hash.Write([]byte("velociportal/shared-application/v1\x00"))
	var length [4]byte
	for _, field := range fields {
		// All fields are already bounded canonical values from the binding,
		// typed-ID parser and personal URL evaluator, well below uint32 bytes.
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), true
}
