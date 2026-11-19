package main

import (
	"crypto/hmac"
	"net/http"
)

// These typed inputs do not decode wire objects. The later adapter must reject
// duplicate, null, additional and case-variant fields before constructing them.
type mcpSharedIconRequest struct {
	Target sharedTargetRef `json:"target"`
	sharedMutationCAS
	Icon string `json:"icon"`
}

type mcpSharedResetRequest struct {
	Target sharedTargetRef `json:"target"`
	sharedMutationCAS
}

// Pure, explicit version choice. Validate BEFORE transformation: otherwise a
// malformed legacy model could become valid by acquiring v4's representation
// rules. Do not serialize the original here: a valid compact source may need a
// shrinking edit before its canonical encoding fits the output bound.
func sharedIconDocumentV4(document serviceMetadataDocument, binding serviceMetadataBinding) (serviceMetadataDocument, error) {
	if err := validateServiceMetadataDocumentModel(document); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(document)
	if !serviceMetadataBoundVersion(out.Version) {
		out.Version = serviceMetadataVersionV4
		out.Namespace, out.ControlPlane = binding.Namespace, binding.ControlPlane
		out.Machines = []machineIconEntry{}
		out.Applications = []applicationIconEntry{}
	}
	if err := validateServiceMetadataBinding(out, binding); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}

func (o *SharedIconOperations) SetMCPIcon(r *http.Request, req mcpSharedIconRequest) error {
	if !serviceMetadataV4RuntimeEnabled {
		return errMetadataStale
	}
	if !validServiceIcon(req.Icon) {
		return errMetadataInvalidFields
	}
	return o.mutateMCPIcon(r, req.Target, req.sharedMutationCAS, req.Icon)
}

func (o *SharedIconOperations) ResetMCPIcon(r *http.Request, req mcpSharedResetRequest) error {
	if !serviceMetadataV4RuntimeEnabled {
		return errMetadataStale
	}
	return o.mutateMCPIcon(r, req.Target, req.sharedMutationCAS, "")
}

func validateSharedIconMutation(ref sharedTargetRef, cas sharedMutationCAS, icon string) error {
	if ref.Kind != sharedTargetNPM && ref.Kind != sharedTargetMachine && ref.Kind != sharedTargetApplication {
		return errMetadataInvalidFields
	}
	if err := validateSharedTargetRef(ref); err != nil {
		return err
	}
	if cas.Revision == "" || len(cas.Revision) > 256 || cas.TargetRevision == "" || len(cas.TargetRevision) > 256 || icon != "" && !validServiceIcon(icon) {
		return errMetadataInvalidFields
	}
	return nil
}

// The ONLY new whole-commit path is itself gated, not an ungated fixture entry.
// Admission derives and pins the caller from the actual request. Application
// holds personal.mu across shared commit; NPM/Machine never touch private state.
func (o *SharedIconOperations) mutateMCPIcon(r *http.Request, ref sharedTargetRef, cas sharedMutationCAS, icon string) error {
	if !serviceMetadataV4RuntimeEnabled {
		return errMetadataStale
	}
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return err
	}
	login := identity.Login
	if err := validateSharedIconMutation(ref, cas, icon); err != nil {
		return err
	}
	if ref.Kind == sharedTargetApplication && o.personal != nil {
		o.personal.mu.Lock()
		defer o.personal.mu.Unlock()
		at := o.now()
		if _, err := o.mcpTargetIdentity(r, login); err != nil {
			return err
		}
		if !o.sharedTargetFresh(o.cache.Get(), at) {
			return errMetadataStale
		}
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	document, err := o.prepareMCPIconLocked(r, ref, cas, icon, login)
	if err != nil {
		return err
	}
	if !serviceMetadataV4RuntimeEnabled {
		return errMetadataStale
	}
	// Only this explicit icon operation may request a legacy-to-v4 transition.
	// The writer independently validates the kind and bound runtime candidate.
	return o.editor.commit(cas.Revision, document, serviceMetadataWriteIcon, func() error {
		if !serviceMetadataV4RuntimeEnabled {
			return errMetadataStale
		}
		return o.recheckMCPIconLocked(r, ref, cas, login)
	})
}

// Caller owns editor.mu, and personal.mu FIRST for an application. This prepares
// only a candidate; it cannot write, install a callback or acknowledge a save.
// login is the operation's pinned entry caller, never adapter-supplied authority.
func (o *SharedIconOperations) prepareMCPIconLocked(r *http.Request, ref sharedTargetRef, cas sharedMutationCAS, icon, login string) (serviceMetadataDocument, error) {
	if err := validateSharedIconMutation(ref, cas, icon); err != nil {
		return serviceMetadataDocument{}, err
	}
	if login == "" {
		return serviceMetadataDocument{}, errMetadataForbidden
	}
	selected, err := o.selectedMCPTargetLocked(r, ref, login, cas.TargetRevision)
	if err != nil {
		return serviceMetadataDocument{}, err
	}
	view := o.editor.view.Load()
	// Visibility/ownership precedes BOTH conflicts. commit repeats whole-byte CAS.
	if !hmac.Equal([]byte(cas.Revision), []byte(view.revision)) {
		return serviceMetadataDocument{}, errMetadataConflict
	}
	document, err := sharedIconDocumentV4(view.metadata.document, o.binding)
	if err != nil {
		return serviceMetadataDocument{}, errMetadataInvalidFields
	}
	switch selected.kind {
	case sharedTargetNPM:
		document, err = mutateServiceMetadataIcon(document, selected.npm.ID, icon)
	case sharedTargetMachine:
		document, err = mutateMachineMetadataIcon(document, selected.deviceID, icon)
	case sharedTargetApplication:
		document, err = mutateApplicationMetadataIcon(document, selected.applicationDigest, icon)
	default:
		return serviceMetadataDocument{}, errMetadataNotVisible
	}
	// Existing pure mutators validate the entire final encoding/aggregate limit.
	if err != nil {
		return serviceMetadataDocument{}, errMetadataInvalidFields
	}
	return document, nil
}

// Mandatory concrete final authority, separately testable below the literal
// production gate. It reuses selection, never a copied identity/target receipt.
// Application owned reads may take storage.mu transiently; this caller never
// already owns storage.mu and never saves or advances a private revision.
func (o *SharedIconOperations) recheckMCPIconLocked(r *http.Request, ref sharedTargetRef, cas sharedMutationCAS, login string) error {
	if err := validateSharedIconMutation(ref, cas, ""); err != nil {
		return err
	}
	if login == "" {
		return errMetadataForbidden
	}
	_, err := o.selectedMCPTargetLocked(r, ref, login, cas.TargetRevision)
	return err
}
