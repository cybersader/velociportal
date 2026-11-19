package main

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type sharedTargetKind string

const (
	sharedTargetNPM         sharedTargetKind = "npm"
	sharedTargetMachine     sharedTargetKind = "machine"
	sharedTargetApplication sharedTargetKind = "application"
)

type sharedTargetRef struct {
	Kind   sharedTargetKind `json:"kind"`
	Handle string           `json:"handle"`
}

type sharedMutationCAS struct {
	Revision       string `json:"revision"`
	TargetRevision string `json:"target_revision"`
}

type sharedTargetSummary struct {
	Target        sharedTargetRef `json:"target"`
	Name          string          `json:"name"`
	SharedIcon    string          `json:"shared_icon"`
	EffectiveIcon string          `json:"effective_icon"`
}

type sharedTargetForm struct {
	sharedTargetSummary
	sharedMutationCAS
}

// Only one kind's evidence is populated. Never serialize or log this internal
// selection. It is neither a catalog nor a retained authority receipt.
type sharedSelectedTarget struct {
	kind              sharedTargetKind
	npm               ProxyHost
	machine           machineMatchEvidence
	deviceID          ProviderDeviceID
	application       PersonalService
	personalState     PersonalServicesState
	ownerID           string
	applicationDigest string
	summary           sharedTargetSummary
	targetRevision    string
}

// Typed validation is not a replacement for a future strict wire decoder.
// Unsupported kinds are indistinguishable from a missing target after admission;
// malformed opaque handles carry no target information.
func validateSharedTargetRef(ref sharedTargetRef) error {
	if ref.Kind != sharedTargetNPM && ref.Kind != sharedTargetMachine && ref.Kind != sharedTargetApplication {
		return errMetadataNotVisible
	}
	if len(ref.Handle) != 43 {
		return errMetadataInvalidFields
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(ref.Handle)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != ref.Handle {
		return errMetadataInvalidFields
	}
	return nil
}

// The literal reader gate precedes even admission, locks, clock and disk. There
// is no production caller/registration until coherent activation in a later task.
func (o *SharedIconOperations) GetMCPTarget(r *http.Request, ref sharedTargetRef) (sharedTargetForm, error) {
	if !serviceMetadataV4RuntimeEnabled {
		return sharedTargetForm{}, errMetadataStale
	}
	return o.prepareMCPTarget(r, ref)
}

// prepareMCPTarget is the dormant guarded read preparation. Package fixtures can
// exercise it below the immutable runtime gate; it has no writer, auth callback,
// transport-selected identity, or public ungated entry. NPM/Machine need only
// editor.mu, even when independent private storage is absent or uncertain.
func (o *SharedIconOperations) prepareMCPTarget(r *http.Request, ref sharedTargetRef) (sharedTargetForm, error) {
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return sharedTargetForm{}, err
	}
	login := identity.Login
	if ref.Kind == sharedTargetApplication && o.personal != nil {
		// Application alone pins its own state through selection, disk inspection
		// and final recheck. Defer order releases editor before personal. No path
		// already holding editor.mu may acquire personal.mu.
		o.personal.mu.Lock()
		defer o.personal.mu.Unlock()
		at := o.now()
		if _, err := o.mcpTargetIdentity(r, login); err != nil {
			return sharedTargetForm{}, err
		}
		if !o.sharedTargetFresh(o.cache.Get(), at) {
			return sharedTargetForm{}, errMetadataStale
		}
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	selected, err := o.selectedMCPTargetLocked(r, ref, login, "")
	if err != nil {
		return sharedTargetForm{}, err
	}
	view := o.editor.view.Load()
	diskErr := o.editor.checkDisk(view)
	// Slow disk inspection is never an authority checkpoint. Repeat request,
	// writer, freshness and current visibility before returning even a conflict.
	current, err := o.selectedMCPTargetLocked(r, ref, login, selected.targetRevision)
	if err != nil {
		return sharedTargetForm{}, err
	}
	if diskErr != nil {
		return sharedTargetForm{}, diskErr
	}
	if o.editor.view.Load() != view {
		return sharedTargetForm{}, errMetadataConflict
	}
	return sharedTargetForm{sharedTargetSummary: current.summary,
		sharedMutationCAS: sharedMutationCAS{Revision: view.revision, TargetRevision: current.targetRevision}}, nil
}

// Caller holds editor.mu and, for application with a personal store, personal.mu
// acquired FIRST. Both cache and clock are obtained after waits/disk;
// the concrete guard pins the original login again, not a previously admitted
// Identity. Current authority is established before target revision comparison.
func (o *SharedIconOperations) selectedMCPTargetLocked(r *http.Request, ref sharedTargetRef, login, expectedRevision string) (sharedSelectedTarget, error) {
	now := o.now()
	identity, err := o.mcpTargetIdentity(r, login)
	if err != nil {
		return sharedSelectedTarget{}, err
	}
	if err := o.checkWriterLocked(); err != nil {
		return sharedSelectedTarget{}, err
	}
	// A real held writer, not merely a presentation view, is required. checkDisk
	// subsequently verifies the held directory and lifetime lock identities.
	if o.editor.directory == nil || o.editor.lock == nil {
		return sharedSelectedTarget{}, errMetadataStale
	}
	data := o.editor.requestData(o.cache.Get())
	if !o.sharedTargetFresh(data, now) {
		return sharedSelectedTarget{}, errMetadataStale
	}
	if err := validateSharedTargetRef(ref); err != nil {
		return sharedSelectedTarget{}, err
	}
	var selected sharedSelectedTarget
	switch ref.Kind {
	case sharedTargetNPM:
		selected, err = o.selectSharedNPM(identity, data, ref)
	case sharedTargetMachine:
		selected, err = o.selectSharedMachine(identity, data, ref)
	case sharedTargetApplication:
		selected, err = o.selectSharedApplicationLocked(identity, data, ref)
		// The owned read can wait on storage.mu. Its post-read clock/freshness
		// check is followed by concrete request/config/writer checks, not a
		// reusable admission receipt or a transport-supplied callback.
		if _, guardErr := o.mcpTargetIdentity(r, login); guardErr != nil {
			return sharedSelectedTarget{}, guardErr
		}
		if writerErr := o.checkWriterLocked(); writerErr != nil {
			return sharedSelectedTarget{}, writerErr
		}
	}
	if err != nil {
		return sharedSelectedTarget{}, err
	}
	if expectedRevision != "" && !hmac.Equal([]byte(expectedRevision), []byte(selected.targetRevision)) {
		return sharedSelectedTarget{}, errMetadataConflict
	}
	return selected, nil
}

// Every payload is an unambiguous deterministic JSON string array. These domains
// do not change the incumbent browser target revision or application digest.
func (o *SharedIconOperations) sharedTargetDigest(domain, login string, kind sharedTargetKind, evidence ...string) string {
	fields := []string{login, o.binding.Namespace, string(o.binding.ControlPlane), string(kind)}
	fields = append(fields, evidence...)
	payload, _ := json.Marshal(fields) // strings only: cannot fail
	return o.editor.digest(domain, payload)
}

func (o *SharedIconOperations) sharedTargetHandle(login string, kind sharedTargetKind, reference ...string) sharedTargetRef {
	return sharedTargetRef{Kind: kind, Handle: o.sharedTargetDigest("shared-target-handle-v1", login, kind, reference...)}
}

// Bound display-only text independently of upstream domain lengths. No new name
// normalizer or target matcher: matching already produced the source card.
func sharedTargetDisplayName(name, fallback string) string {
	if !utf8.ValidString(name) {
		return fallback
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fallback
	}
	runes := []rune(name)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return name
}

func (o *SharedIconOperations) selectSharedNPM(identity *Identity, data *CacheData, ref sharedTargetRef) (sharedSelectedTarget, error) {
	var selected *serviceMatchEvidence
	for _, match := range evaluateServices(identity, data) {
		if match.ProxyHost.ID <= 0 {
			continue
		}
		candidate := o.sharedTargetHandle(identity.Login, sharedTargetNPM, strconv.Itoa(match.ProxyHost.ID))
		if !hmac.Equal([]byte(candidate.Handle), []byte(ref.Handle)) {
			continue
		}
		if selected != nil {
			return sharedSelectedTarget{}, errMetadataNotVisible
		}
		copy := match
		selected = &copy
	}
	if selected == nil {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	return o.sharedNPMSummary(identity, data, ref, *selected)
}

// Both selected-get and listing call this only after full, unique authorization.
func (o *SharedIconOperations) sharedNPMSummary(identity *Identity, data *CacheData, ref sharedTargetRef, selected serviceMatchEvidence) (sharedSelectedTarget, error) {
	// Only after full visible/renderable selection may shared metadata be read.
	override := data.ServiceMetadata.Overrides[selected.ProxyHost.ID]
	card := selected.Card
	if override.Icon != "" && !validServiceIcon(override.Icon) || card.Icon != "" && !validServiceIcon(card.Icon) {
		return sharedSelectedTarget{}, errMetadataStale
	}
	summary := sharedTargetSummary{Target: ref, Name: sharedTargetDisplayName(card.Name, "Service"), SharedIcon: override.Icon, EffectiveIcon: card.Icon}
	order := ""
	if card.Order != nil {
		order = strconv.Itoa(*card.Order)
	}
	evidence := []string{o.editor.targetRevision(selected.ProxyHost), override.Name, override.URL, override.Icon,
		card.Name, card.URL, card.Domain, string(card.LinkState), card.Icon, card.Category, order}
	if override.CustomIcon != "" {
		evidence = append(evidence, "custom_icon", override.CustomIcon)
	}
	revision := o.sharedTargetDigest("shared-target-revision-v1", identity.Login, sharedTargetNPM, evidence...)
	return sharedSelectedTarget{kind: sharedTargetNPM, npm: selected.ProxyHost, summary: summary, targetRevision: revision}, nil
}

// Canonicalize only the detached target-revision view before the existing
// matcher reduces overlapping checks by account. Shortest effective check
// period wins here, with an omitted period meaning 12 hours. This is a stable
// revision convention, not a claim about live SSH enforcement. Neither the
// cached policy nor the portal's incumbent matcher/rendering is changed.
func sharedMachineRevisionData(data *CacheData) *CacheData {
	if data == nil || data.Policy == nil {
		return data
	}
	local := *data
	policy := *data.Policy
	policy.SSH.Rules = append([]SSHRule(nil), data.Policy.SSH.Rules...)
	period := func(rule SSHRule) time.Duration {
		if rule.Action == "check" && rule.CheckPeriod == 0 {
			return 12 * time.Hour
		}
		return rule.CheckPeriod
	}
	sort.SliceStable(policy.SSH.Rules, func(i, j int) bool {
		return period(policy.SSH.Rules[i]) < period(policy.SSH.Rules[j])
	})
	local.Policy = &policy
	return &local
}

func (o *SharedIconOperations) selectSharedMachine(identity *Identity, data *CacheData, ref sharedTargetRef) (sharedSelectedTarget, error) {
	if o.binding.ControlPlane != controlPlaneTailscale {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	var selected *machineMatchEvidence
	var key ProviderDeviceID
	for _, match := range evaluateMachines(identity, sharedMachineRevisionData(data)) {
		id, ok := data.PresentationDeviceIDsByNodeID[match.Node.ID]
		if !ok || id.Provider != controlPlaneTailscale || !validPresentationDeviceID(id.Kind, id.Value) {
			continue
		}
		candidate := o.sharedTargetHandle(identity.Login, sharedTargetMachine, string(id.Kind), id.Value)
		if !hmac.Equal([]byte(candidate.Handle), []byte(ref.Handle)) {
			continue
		}
		if selected != nil {
			return sharedSelectedTarget{}, errMetadataNotVisible
		}
		copy := match
		selected, key = &copy, id
	}
	if selected == nil {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	// Defensive uniqueness over current nodes and the typed side projection also
	// rejects collisions with a currently invisible node. Never consult stored
	// artwork to discover a target or choose first/last among ambiguous records.
	if !sharedMachineUniqueNodes(data)[selected.Node.ID] {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	return o.sharedMachineSummary(identity, data, ref, *selected, key)
}

// Build uniqueness once per projection, including invisible nodes and orphaned
// side keys. A page must not rescan all nodes for each already-matched machine.
func sharedMachineUniqueNodes(data *CacheData) map[string]bool {
	nodes := make(map[string]int)
	keys := make(map[ProviderDeviceID]int)
	sideKeys := make(map[ProviderDeviceID]int)
	for _, node := range data.Nodes {
		nodes[node.ID]++
		keys[data.PresentationDeviceIDsByNodeID[node.ID]]++
	}
	for _, key := range data.PresentationDeviceIDsByNodeID {
		sideKeys[key]++
	}
	unique := make(map[string]bool)
	for nodeID, count := range nodes {
		key, ok := data.PresentationDeviceIDsByNodeID[nodeID]
		unique[nodeID] = ok && count == 1 && keys[key] == 1 && sideKeys[key] == 1
	}
	return unique
}

func (o *SharedIconOperations) sharedMachineSummary(identity *Identity, data *CacheData, ref sharedTargetRef, selected machineMatchEvidence, key ProviderDeviceID) (sharedSelectedTarget, error) {
	name, _, canonicalName := machineCardNames(selected.Card.Target)
	if !canonicalName {
		name = "Machine"
	} // Do not emit an IP fallback as a response name.
	icon := machinePresentationIcons([]MachineCard{selected.Card}, data, o.binding)[selected.Card.ID]
	summary := sharedTargetSummary{Target: ref, Name: sharedTargetDisplayName(name, "Machine"), SharedIcon: icon, EffectiveIcon: icon}
	access := make([]string, 0, len(selected.Card.Access))
	for _, a := range selected.Card.Access {
		period := a.CheckPeriod
		if a.Action == "check" && period == 0 {
			period = 12 * time.Hour
		}
		encoded, _ := json.Marshal([]string{a.User, a.Action, strconv.FormatInt(int64(period), 10)})
		access = append(access, string(encoded))
	}
	sort.Strings(access)
	evidence := []string{string(key.Kind), key.Value, selected.Card.Name, selected.Card.Target, summary.Name, icon, "sshEnabled=true", "blocksIncomingConnections=false"}
	evidence = append(evidence, access...)
	revision := o.sharedTargetDigest("shared-target-revision-v1", identity.Login, sharedTargetMachine, evidence...)
	return sharedSelectedTarget{kind: sharedTargetMachine, machine: selected, deviceID: key, summary: summary, targetRevision: revision}, nil
}

func (o *SharedIconOperations) sharedTargetFresh(data *CacheData, at time.Time) bool {
	return data != nil && data.Policy != nil && o.interval > 0 && o.interval <= time.Duration(1<<63-1)/3 &&
		!data.UpdatedAt.IsZero() && !data.UpdatedAt.After(at) && at.Sub(data.UpdatedAt) <= 3*o.interval &&
		data.ControlPlane.Provider == o.binding.ControlPlane
}

// The common/MCP origin is captured at construction. Personal configuration is
// already an immutable store-owned copy; validate it against that common origin
// on every selection, never infer enablement or provision a private file for RAM.
func (o *SharedIconOperations) applicationConfigLocked() bool {
	s := o.personal
	if s == nil || s.closed || o.binding.ControlPlane != controlPlaneTailscale {
		return false
	}
	host, err := validatePortalPublicOrigin(s.config.Origin)
	if err != nil || host != s.config.Host || s.config.Origin != o.origin || host != o.host {
		return false
	}
	if s.storage == nil {
		return s.config.File == "" && s.config.Namespace == ""
	}
	return s.config.File != "" && s.config.Namespace == o.binding.Namespace
}

// Caller holds personal.mu before editor.mu. Only this exact login's current
// cloned state participates. Ordinary volatile management deliberately remains
// less restrictive; fresh owner-ID authority is mandatory for this NEW operation.
type sharedApplicationRead struct {
	state PersonalServicesState
	data  *CacheData
	owner string
	at    time.Time
}

// This single owned-read seam is shared by selected-get and kind-specific pages.
// It never reads another login or acquires personal.mu recursively.
func (o *SharedIconOperations) readSharedApplicationLocked(identity *Identity, data *CacheData) (sharedApplicationRead, error) {
	if !o.applicationConfigLocked() {
		return sharedApplicationRead{}, errMetadataNotVisible
	}
	state, readErr := o.personal.readForSnapshotLocked(identity.Login, data, o.interval, o.now)
	// The owned read may wait on storage while the cache publishes a replacement.
	// Re-establish authority from the current snapshot, not the captured pointer.
	// The returned state was bound using readOwner; a new owner must not adopt it.
	// All subsequent destination and revision evidence uses this same fresh view.
	at := o.now()
	readOwner, readOwnerErr := personalOwner(identity.Login, data, at, o.interval)
	data = o.editor.requestData(o.cache.Get())
	if !o.sharedTargetFresh(data, at) {
		return sharedApplicationRead{}, errMetadataStale
	}
	owner, ownerErr := personalOwner(identity.Login, data, at, o.interval)
	if readOwnerErr != nil || ownerErr != nil || readOwner != owner || !o.applicationConfigLocked() {
		return sharedApplicationRead{}, errMetadataNotVisible
	}
	if errors.Is(readErr, errPersonalStorageUncertain) {
		return sharedApplicationRead{}, errMetadataUncertain
	}
	if readErr != nil {
		return sharedApplicationRead{}, errMetadataNotVisible
	}
	return sharedApplicationRead{state: state, data: data, owner: owner, at: at}, nil
}

func (o *SharedIconOperations) selectSharedApplicationLocked(identity *Identity, data *CacheData, ref sharedTargetRef) (sharedSelectedTarget, error) {
	read, err := o.readSharedApplicationLocked(identity, data)
	if err != nil {
		return sharedSelectedTarget{}, err
	}
	state, data, owner, at := read.state, read.data, read.owner, read.at
	if !state.Initialized {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	evaluator := newPersonalServiceEvaluator(identity, data, at, o.interval)
	var selected *PersonalService
	var evidence personalServiceEvidence
	var digest string
	for _, entry := range state.Document.Entries {
		current, active := evaluator.evaluate(entry)
		if !active {
			continue
		}
		derived, ok := personalApplicationDigest(o.binding, data, current)
		if !ok {
			continue
		}
		candidate := o.sharedTargetHandle(identity.Login, sharedTargetApplication, entry.ID, derived)
		if !hmac.Equal([]byte(candidate.Handle), []byte(ref.Handle)) {
			continue
		}
		if selected != nil {
			return sharedSelectedTarget{}, errMetadataNotVisible
		}
		copy := entry
		selected, evidence, digest = &copy, current, derived
	}
	if selected == nil {
		return sharedSelectedTarget{}, errMetadataNotVisible
	}
	return o.sharedApplicationSummary(identity, data, ref, *selected, evidence, digest, state, owner)
}

func (o *SharedIconOperations) sharedApplicationSummary(identity *Identity, data *CacheData, ref sharedTargetRef, selected PersonalService, evidence personalServiceEvidence, digest string, state PersonalServicesState, owner string) (sharedSelectedTarget, error) {
	// Only a current owned active selection may consult the shared digest index.
	shared := ""
	if metadata := sharedIconMetadata(data, o.binding); metadata != nil {
		shared = metadata.ApplicationIcons[digest]
	}
	effective := selected.Icon
	if effective == "" {
		effective = shared
	}
	if effective == "" {
		effective = "generic"
	}
	if shared != "" && !validServiceIcon(shared) || !validServiceIcon(effective) {
		return sharedSelectedTarget{}, errMetadataStale
	}
	key := data.PresentationDeviceIDsByNodeID[evidence.node.ID]
	summary := sharedTargetSummary{Target: ref, Name: sharedTargetDisplayName(selected.Name, "Application"), SharedIcon: shared, EffectiveIcon: effective}
	revision := o.sharedTargetDigest("shared-target-revision-v1", identity.Login, sharedTargetApplication,
		owner, strconv.FormatBool(state.Durable), state.StoreID, state.Generation, state.Revision,
		selected.ID, selected.URL, selected.Name, selected.Icon, string(key.Kind), key.Value,
		evidence.hostname, strconv.Itoa(evidence.port), digest, summary.Name, shared, effective)
	return sharedSelectedTarget{kind: sharedTargetApplication, application: selected, personalState: state,
		ownerID: owner, deviceID: key, applicationDigest: digest, summary: summary, targetRevision: revision}, nil
}
