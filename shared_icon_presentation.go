package main

// presentationData checks the request-local committed metadata against the
// immutable runtime binding and the SAME complete provider snapshot. Configured
// readers still reject unsupported versions/mismatched binding before publication;
// this defensive boundary neither loads files nor bypasses that runtime gate.
// A mismatched v4 view cannot decorate any source, including incumbent NPM fields.
func presentationData(data *CacheData, binding serviceMetadataBinding) *CacheData {
	if data == nil || data.ServiceMetadata == nil || data.ServiceMetadata.document.Version != serviceMetadataVersionV4 {
		return data
	}
	if data.ControlPlane.Provider == binding.ControlPlane && validateServiceMetadataBinding(data.ServiceMetadata.document, binding) == nil {
		return data
	}
	local := *data
	local.ServiceMetadata = nil
	return &local
}

func sharedIconMetadata(data *CacheData, binding serviceMetadataBinding) *ServiceMetadata {
	data = presentationData(data, binding)
	if data == nil || data.ServiceMetadata == nil || data.ServiceMetadata.document.Version != serviceMetadataVersionV4 {
		return nil
	}
	return data.ServiceMetadata
}

// Only the caller's already active personal projection supplies evidence here.
// The resolved icon is a detached card field, never an authored or recovery value.
func personalPresentationIcon(authored string, data *CacheData, binding serviceMetadataBinding, evidence personalServiceEvidence) string {
	if authored != "" {
		return authored
	}
	if metadata := sharedIconMetadata(data, binding); metadata != nil {
		if digest, ok := personalApplicationDigest(binding, data, evidence); ok {
			if icon := metadata.ApplicationIcons[digest]; validServiceIcon(icon) {
				return icon
			}
		}
	}
	return "" // The incumbent personal renderer supplies its generic fallback.
}

// cards must be the result of the full MatchMachines projection for this request.
// Artwork is not evidence: this map contains only already-visible cards and does
// not change their target, accounts, commands, console eligibility or stale rules.
func machinePresentationIcons(cards []MachineCard, data *CacheData, binding serviceMetadataBinding) map[string]string {
	metadata := sharedIconMetadata(data, binding)
	if metadata == nil || binding.ControlPlane != controlPlaneTailscale {
		return nil
	}
	icons := make(map[string]string)
	for _, card := range cards {
		id, ok := data.PresentationDeviceIDsByNodeID[card.ID]
		if !ok || id.Provider != binding.ControlPlane || !validPresentationDeviceID(id.Kind, id.Value) {
			continue
		}
		if icon := metadata.MachineIcons[id]; validServiceIcon(icon) {
			icons[card.ID] = icon
		}
	}
	return icons
}
