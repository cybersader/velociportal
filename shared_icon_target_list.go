package main

import (
	"crypto/hmac"
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
)

type sharedTargetListRequest struct {
	Kind   sharedTargetKind `json:"kind"`
	Cursor string           `json:"cursor,omitempty"`
	Limit  int              `json:"limit,omitempty"`
}

type sharedTargetPage struct {
	Targets    []sharedTargetSummary `json:"targets"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

// No production registration. The literal gate precedes admission, clock, locks,
// body or storage effects, including on a nil receiver.
func (o *SharedIconOperations) ListMCPTargets(r *http.Request, req sharedTargetListRequest) (sharedTargetPage, error) {
	if !serviceMetadataV4RuntimeEnabled {
		return sharedTargetPage{}, errMetadataStale
	}
	return o.prepareMCPTargetList(r, req)
}

func sharedTargetListLimit(req sharedTargetListRequest) (int, error) {
	if req.Kind != sharedTargetNPM && req.Kind != sharedTargetMachine && req.Kind != sharedTargetApplication || req.Limit < 0 || req.Limit > 64 {
		return 0, errMetadataInvalidFields
	}
	// Typed zero represents absence only. The later strict wire adapter must
	// reject supplied zero, null, aliases, duplicate keys and noncanonical integers.
	if req.Limit == 0 {
		return 64, nil
	}
	return req.Limit, nil
}

// Fixed binary layout: version byte, 32-byte opaque handle, 32-byte MAC. There
// are no JSON fields/aliases, offsets, counts, source IDs or plaintext preimages.
// Strict unpadded base64url is exactly 87 bytes (well below the 256-byte ceiling).
func (o *SharedIconOperations) sharedTargetCursor(login string, kind sharedTargetKind, handle string) string {
	raw, _ := base64.RawURLEncoding.DecodeString(handle) // only generated handles
	mac := o.sharedTargetDigest("shared-target-cursor-v1", login, kind, "1", handle)
	tag, _ := base64.RawURLEncoding.DecodeString(mac)
	payload := append([]byte{1}, raw...)
	payload = append(payload, tag...)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func (o *SharedIconOperations) sharedTargetCursorAnchor(login string, kind sharedTargetKind, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 256 || len(cursor) != 87 {
		return "", errMetadataInvalidFields
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || len(raw) != 65 || raw[0] != 1 || base64.RawURLEncoding.EncodeToString(raw) != cursor {
		return "", errMetadataInvalidFields
	}
	handle := base64.RawURLEncoding.EncodeToString(raw[1:33])
	if !hmac.Equal([]byte(cursor), []byte(o.sharedTargetCursor(login, kind, handle))) {
		return "", errMetadataInvalidFields
	}
	return handle, nil
}

// Each projection uses the incumbent matchers once, never one full matching pass
// or owned private read per target. Metadata indexes are consulted only after
// current authorization and ambiguity checks, never as discovery indexes.
func (o *SharedIconOperations) projectSharedTargetsLocked(r *http.Request, kind sharedTargetKind, login string) ([]sharedSelectedTarget, error) {
	at := o.now()
	identity, err := o.mcpTargetIdentity(r, login)
	if err != nil {
		return nil, err
	}
	if err := o.checkWriterLocked(); err != nil {
		return nil, err
	}
	if o.editor.directory == nil || o.editor.lock == nil {
		return nil, errMetadataStale
	}
	data := o.editor.requestData(o.cache.Get())
	if !o.sharedTargetFresh(data, at) {
		return nil, errMetadataStale
	}
	targets := make([]sharedSelectedTarget, 0)
	switch kind {
	case sharedTargetNPM:
		matches := evaluateServices(identity, data)
		counts := make(map[int]int)
		for _, match := range matches {
			counts[match.ProxyHost.ID]++
		}
		for _, match := range matches {
			id := match.ProxyHost.ID
			if id <= 0 || counts[id] != 1 {
				continue
			}
			ref := o.sharedTargetHandle(login, kind, strconv.Itoa(id))
			selected, err := o.sharedNPMSummary(identity, data, ref, match)
			if err != nil {
				return nil, err
			}
			targets = append(targets, selected)
		}
	case sharedTargetMachine:
		if o.binding.ControlPlane == controlPlaneTailscale {
			matches := evaluateMachines(identity, sharedMachineRevisionData(data))
			unique := sharedMachineUniqueNodes(data)
			counts := make(map[ProviderDeviceID]int)
			for _, match := range matches {
				counts[data.PresentationDeviceIDsByNodeID[match.Node.ID]]++
			}
			for _, match := range matches {
				key, ok := data.PresentationDeviceIDsByNodeID[match.Node.ID]
				if !ok || key.Provider != controlPlaneTailscale || !validPresentationDeviceID(key.Kind, key.Value) || !unique[match.Node.ID] || counts[key] != 1 {
					continue
				}
				ref := o.sharedTargetHandle(login, kind, string(key.Kind), key.Value)
				selected, err := o.sharedMachineSummary(identity, data, ref, match, key)
				if err != nil {
					return nil, err
				}
				targets = append(targets, selected)
			}
		}
	case sharedTargetApplication:
		read, readErr := o.readSharedApplicationLocked(identity, data)
		// The owned read can wait on storage.mu; repeat concrete admission and
		// writer checks even when it failed. The shared seam reloads CURRENT cache
		// and requires old/current owner equality before exposing returned state.
		if _, err := o.mcpTargetIdentity(r, login); err != nil {
			return nil, err
		}
		if err := o.checkWriterLocked(); err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, readErr
		}
		if read.state.Initialized {
			evaluator := newPersonalServiceEvaluator(identity, read.data, read.at, o.interval)
			unique := sharedMachineUniqueNodes(read.data)
			counts := make(map[string]int)
			for _, entry := range read.state.Document.Entries {
				counts[entry.ID]++
			}
			for _, entry := range read.state.Document.Entries {
				if counts[entry.ID] != 1 {
					continue
				}
				evidence, active := evaluator.evaluate(entry)
				if !active || !unique[evidence.node.ID] {
					continue
				}
				digest, ok := personalApplicationDigest(o.binding, read.data, evidence)
				if !ok {
					continue
				}
				ref := o.sharedTargetHandle(login, kind, entry.ID, digest)
				selected, err := o.sharedApplicationSummary(identity, read.data, ref, entry, evidence, digest, read.state, read.owner)
				if err != nil {
					return nil, err
				}
				targets = append(targets, selected)
			}
		}
	default:
		return nil, errMetadataInvalidFields
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].summary.Target.Handle < targets[j].summary.Target.Handle })
	for i := 1; i < len(targets); i++ {
		if targets[i-1].summary.Target.Handle == targets[i].summary.Target.Handle {
			return nil, errMetadataConflict // no first winner, even for a handle collision
		}
	}
	return targets, nil
}

func (o *SharedIconOperations) sharedTargetPage(login string, kind sharedTargetKind, anchor string, limit int, targets []sharedSelectedTarget) (sharedTargetPage, error) {
	start := 0
	if anchor != "" {
		i := sort.Search(len(targets), func(i int) bool { return targets[i].summary.Target.Handle >= anchor })
		if i == len(targets) || targets[i].summary.Target.Handle != anchor {
			return sharedTargetPage{}, errMetadataConflict // reload, no disappearance reason
		}
		start = i + 1
	}
	page := sharedTargetPage{Targets: make([]sharedTargetSummary, 0)}
	for i := start; i < len(targets) && len(page.Targets) < limit; i++ {
		candidate := sharedTargetPage{Targets: append(page.Targets, targets[i].summary)}
		if i+1 < len(targets) {
			candidate.NextCursor = o.sharedTargetCursor(login, kind, targets[i].summary.Target.Handle)
		}
		size, err := mcpToolResultSizeBound(candidate)
		if err != nil || size > mcpMaxResponseBytes {
			if len(page.Targets) == 0 {
				return sharedTargetPage{}, errMetadataStale
			}
			// The previously accepted page already budgeted its continuation.
			break
		}
		page = candidate
	}
	return page, nil
}

// Dormant guarded preparation only: actual request retained, original login
// pinned, no callback/permission flag, no mutation/revision advance. Applications
// alone take personal -> editor with transient owned storage reads.
func (o *SharedIconOperations) prepareMCPTargetList(r *http.Request, req sharedTargetListRequest) (sharedTargetPage, error) {
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return sharedTargetPage{}, err
	}
	login := identity.Login
	limit, err := sharedTargetListLimit(req)
	if err != nil {
		return sharedTargetPage{}, err
	}
	anchor, err := o.sharedTargetCursorAnchor(login, req.Kind, req.Cursor)
	if err != nil {
		return sharedTargetPage{}, err
	}
	if req.Kind == sharedTargetApplication && o.personal != nil {
		o.personal.mu.Lock()
		defer o.personal.mu.Unlock()
		at := o.now()
		if _, err := o.mcpTargetIdentity(r, login); err != nil {
			return sharedTargetPage{}, err
		}
		if !o.sharedTargetFresh(o.cache.Get(), at) {
			return sharedTargetPage{}, errMetadataStale
		}
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	targets, err := o.projectSharedTargetsLocked(r, req.Kind, login)
	if err != nil {
		return sharedTargetPage{}, err
	}
	// Establish current authority, including the cursor anchor, before disk CAS.
	if _, err := o.sharedTargetPage(login, req.Kind, anchor, limit, targets); err != nil {
		return sharedTargetPage{}, err
	}
	view := o.editor.view.Load()
	diskErr := o.editor.checkDisk(view)
	current, err := o.projectSharedTargetsLocked(r, req.Kind, login)
	if err != nil {
		return sharedTargetPage{}, err
	}
	page, err := o.sharedTargetPage(login, req.Kind, anchor, limit, current)
	if err != nil {
		return sharedTargetPage{}, err
	}
	// Assembly/encoding is slow work too. Reproject once more, including the final
	// owned read and current-cache reload for applications. No retry or stale page
	// on a changed eligible set/evidence. Identical fresh publications are stable.
	final, err := o.projectSharedTargetsLocked(r, req.Kind, login)
	if err != nil {
		return sharedTargetPage{}, err
	}
	if _, err := o.sharedTargetPage(login, req.Kind, anchor, limit, final); err != nil {
		return sharedTargetPage{}, err
	}
	if len(current) != len(final) {
		return sharedTargetPage{}, errMetadataConflict
	}
	for i := range current {
		if current[i].summary != final[i].summary || current[i].targetRevision != final[i].targetRevision {
			return sharedTargetPage{}, errMetadataConflict
		}
	}
	if _, err := o.mcpTargetIdentity(r, login); err != nil {
		return sharedTargetPage{}, err
	}
	if err := o.checkWriterLocked(); err != nil {
		return sharedTargetPage{}, err
	}
	if diskErr != nil {
		return sharedTargetPage{}, diskErr
	}
	if o.editor.view.Load() != view {
		return sharedTargetPage{}, errMetadataConflict
	}
	return page, nil
}
