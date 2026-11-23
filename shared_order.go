package main

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"sort"
)

// maxSharedOrderEntries bounds one shared_order_set call. It matches the
// target page size so one listing page can be reordered in one commit.
const maxSharedOrderEntries = 64

// One requested shared default change for an NPM target. Set* distinguishes an
// omitted field (left unchanged) from an explicit null (removed): Set* with a
// nil value removes that field. Neither field ever grants or hides a card.
type mcpSharedOrderEntry struct {
	Target      sharedTargetRef
	SetOrder    bool
	Order       *int
	SetCategory bool
	Category    *string
}

// Typed input only; the strict MCP adapter rejects duplicate, unknown,
// case-variant and malformed fields before constructing it.
type mcpSharedOrderRequest struct {
	Revision string
	Entries  []mcpSharedOrderEntry
}

type sharedOrderChange struct {
	id    int
	entry mcpSharedOrderEntry
}

func validateSharedOrderEntry(entry mcpSharedOrderEntry) error {
	if !entry.SetOrder && !entry.SetCategory {
		return errMetadataInvalidFields
	}
	if entry.Order != nil && (!entry.SetOrder || *entry.Order < 0 || *entry.Order > maxServiceMetadataOrder) {
		return errMetadataInvalidFields
	}
	if entry.Category != nil {
		if !entry.SetCategory {
			return errMetadataInvalidFields
		}
		if _, err := validateServiceMetadataCategory(*entry.Category); err != nil {
			return errMetadataInvalidFields
		}
	}
	return nil
}

func validateSharedOrderRequest(req mcpSharedOrderRequest) error {
	if req.Revision == "" || len(req.Revision) > 256 || len(req.Entries) == 0 || len(req.Entries) > maxSharedOrderEntries {
		return errMetadataInvalidFields
	}
	seen := make(map[sharedTargetRef]bool, len(req.Entries))
	for _, entry := range req.Entries {
		if err := validateSharedTargetRef(entry.Target); err != nil {
			return err
		}
		if seen[entry.Target] {
			return errMetadataInvalidFields
		}
		seen[entry.Target] = true
		if err := validateSharedOrderEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// SetMCPOrder changes the shared default category/order of currently visible
// NPM targets in ONE whole-document CAS commit and returns the new document
// revision. Machine/application targets are indistinguishable from absent.
// It never touches private personal state, so it takes only editor.mu.
func (o *SharedIconOperations) SetMCPOrder(r *http.Request, req mcpSharedOrderRequest) (string, error) {
	if !serviceMetadataV4RuntimeEnabled {
		return "", errMetadataStale
	}
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return "", err
	}
	login := identity.Login
	if err := validateSharedOrderRequest(req); err != nil {
		return "", err
	}
	for _, entry := range req.Entries {
		if entry.Target.Kind != sharedTargetNPM {
			return "", errMetadataNotVisible
		}
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	document, pinned, changed, err := o.prepareMCPOrderLocked(r, req, login)
	if err != nil {
		return "", err
	}
	if !changed {
		// Every target was visible and the revision matched; nothing to write.
		return o.editor.view.Load().revision, nil
	}
	// Ordinary NPM presentation write: legacy->v3, v4->v4, v5->v5. Never a
	// promotion to a bound version, and the writer rechecks that rule.
	err = o.editor.commit(req.Revision, document, serviceMetadataWriteNPM, func() error {
		return o.recheckMCPOrderLocked(r, req, pinned, login)
	})
	if err != nil {
		return "", err
	}
	return o.editor.view.Load().revision, nil
}

// Caller owns editor.mu. Every target is selected with the same current
// authority the icon path uses BEFORE the whole-document CAS is compared, so a
// conflict is never exposed for an invisible target. The returned target
// revisions pin each selection for the final pre-rename recheck.
func (o *SharedIconOperations) prepareMCPOrderLocked(r *http.Request, req mcpSharedOrderRequest, login string) (serviceMetadataDocument, []string, bool, error) {
	if login == "" {
		return serviceMetadataDocument{}, nil, false, errMetadataForbidden
	}
	if err := validateSharedOrderRequest(req); err != nil {
		return serviceMetadataDocument{}, nil, false, err
	}
	changes := make([]sharedOrderChange, 0, len(req.Entries))
	pinned := make([]string, 0, len(req.Entries))
	ids := make(map[int]bool, len(req.Entries))
	for _, entry := range req.Entries {
		selected, err := o.selectedMCPTargetLocked(r, entry.Target, login, "")
		if err != nil {
			return serviceMetadataDocument{}, nil, false, err
		}
		if selected.kind != sharedTargetNPM || selected.npm.ID <= 0 {
			return serviceMetadataDocument{}, nil, false, errMetadataNotVisible
		}
		if ids[selected.npm.ID] {
			return serviceMetadataDocument{}, nil, false, errMetadataInvalidFields
		}
		ids[selected.npm.ID] = true
		changes = append(changes, sharedOrderChange{id: selected.npm.ID, entry: entry})
		pinned = append(pinned, selected.targetRevision)
	}
	view := o.editor.view.Load()
	if !hmac.Equal([]byte(req.Revision), []byte(view.revision)) {
		return serviceMetadataDocument{}, nil, false, errMetadataConflict
	}
	document, changed, err := mutateServiceMetadataOrder(view.metadata.document, changes)
	if err != nil {
		return serviceMetadataDocument{}, nil, false, errMetadataInvalidFields
	}
	return document, pinned, changed, nil
}

// Mandatory final authority immediately before rename: every target must still
// be the same currently visible NPM selection that was prepared.
func (o *SharedIconOperations) recheckMCPOrderLocked(r *http.Request, req mcpSharedOrderRequest, pinned []string, login string) error {
	if login == "" {
		return errMetadataForbidden
	}
	if len(pinned) != len(req.Entries) {
		return errMetadataStale
	}
	for i, entry := range req.Entries {
		selected, err := o.selectedMCPTargetLocked(r, entry.Target, login, pinned[i])
		if err != nil {
			return err
		}
		if selected.kind != sharedTargetNPM {
			return errMetadataNotVisible
		}
	}
	return nil
}

// Pure lossless mutator. Only category/order of the named positive IDs change;
// every other entry, field, array, binding and null presence is retained. A
// touched entry left with no fields is removed. Legacy documents become v3 like
// ordinary browser saves; bound v4/v5 keep their version.
func mutateServiceMetadataOrder(document serviceMetadataDocument, changes []sharedOrderChange) (serviceMetadataDocument, bool, error) {
	invalid := errors.New("invalid shared order change")
	if len(changes) == 0 || len(changes) > maxSharedOrderEntries {
		return serviceMetadataDocument{}, false, invalid
	}
	if err := validateServiceMetadataDocumentModel(document); err != nil {
		return serviceMetadataDocument{}, false, err
	}
	updated := cloneServiceMetadataDocument(document)
	index := make(map[int]int, len(updated.Services)+len(changes))
	for i, row := range updated.Services {
		index[row.ProxyHostID] = i
	}
	touched := make(map[int]bool, len(changes))
	changed := false
	for _, change := range changes {
		if change.id <= 0 || touched[change.id] || validateSharedOrderEntry(change.entry) != nil {
			return serviceMetadataDocument{}, false, invalid
		}
		touched[change.id] = true
		i, ok := index[change.id]
		if !ok {
			updated.Services = append(updated.Services, serviceMetadataEntry{ProxyHostID: change.id})
			i = len(updated.Services) - 1
			index[change.id] = i
		}
		row := &updated.Services[i]
		if change.entry.SetOrder {
			if change.entry.Order == nil {
				if row.Order != nil {
					row.Order, changed = nil, true
				}
			} else if row.Order == nil || *row.Order != *change.entry.Order {
				value := *change.entry.Order
				row.Order, changed = &value, true
			}
		}
		if change.entry.SetCategory {
			if change.entry.Category == nil {
				if row.Category != nil {
					row.Category, changed = nil, true
				}
			} else if row.Category == nil || *row.Category != *change.entry.Category {
				value := *change.entry.Category
				row.Category, changed = &value, true
			}
		}
	}
	if !changed {
		return document, false, nil
	}
	if !serviceMetadataBoundVersion(updated.Version) {
		updated.Version = serviceMetadataVersionV3
	}
	kept := make([]serviceMetadataEntry, 0, len(updated.Services))
	for _, row := range updated.Services {
		empty := row.Name == nil && row.URL == nil && row.Category == nil && row.Order == nil && row.Icon == nil && row.CustomIcon == nil &&
			!(serviceMetadataBoundVersion(updated.Version) && (row.NameNull || row.URLNull))
		if touched[row.ProxyHostID] && empty {
			continue
		}
		kept = append(kept, row)
	}
	updated.Services = kept
	sort.Slice(updated.Services, func(i, j int) bool { return updated.Services[i].ProxyHostID < updated.Services[j].ProxyHostID })
	// The serializer validates the complete model and the output byte budget.
	if _, err := serializeServiceMetadataDocument(updated); err != nil {
		return serviceMetadataDocument{}, false, err
	}
	return updated, true, nil
}
