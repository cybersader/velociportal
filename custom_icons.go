package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/draw"
	"image/png"
	"net/http"
)

// Opt-in custom PNG logos for already-visible NPM service cards (metadata v5).
// Imports arrive only through the guarded shared-editor MCP tools; there is no
// browser upload, URL ingestion, network fetch, filesystem path or raw SVG.
// An imported image grants nothing: it renders only inside markup for a card
// the viewer is already authorized to see, as an inline data: URI.
// Decoded input cap; the base64 cap derived below is checked BEFORE decoding and
// keeps a complete import request inside the unchanged 16 KiB MCP request limit.
const maxCustomIconInputBytes = 8 * 1024

// Canonical (re-encoded) stored size, per icon.
const maxCustomIconBytes = 8 * 1024

// Width and height, each. 128*128 bounds pixel memory before the full decode.
const maxCustomIconDimension = 128

// Distinct stored images, referenced or not: one per card for the current
// ~45-card catalog with headroom. There is no automatic cleanup.
const maxCustomIcons = 64

// Aggregate canonical bytes across every stored image.
const maxCustomIconTotalBytes = 384 * 1024

// Maximum base64 data-URI bytes emitted into one portal response. Cards past the
// budget deterministically fall back to their finite icon.
const maxCustomIconRenderBytes = 512 * 1024

// Only v5 files may exceed the incumbent 256 KiB; the non-image remainder of a
// v5 file still honors maxServiceMetadataBytes.
const maxServiceMetadataReadBytes = 1024 * 1024

var (
	maxCustomIconInputBase64  = base64.StdEncoding.EncodedLen(maxCustomIconInputBytes)
	maxCustomIconStoredBase64 = base64.StdEncoding.EncodedLen(maxCustomIconBytes)
	errCustomIconCapacity     = errors.New("custom icon capacity reached")
	errCustomIconInvalid      = errors.New("custom icon is invalid")
	pngSignature              = []byte("\x89PNG\r\n\x1a\n")
)

type customIconEntry struct {
	ID  string `json:"id"`
	PNG string `json:"png"`
}

// Deterministic content ID: SHA-256 of the canonical PNG bytes.
func validCustomIconID(value string) bool { return validApplicationTargetDigest(value) }

func customIconID(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Raw-array bounds precede typed decoding, mirroring the v4 row validator.
func validateCustomIconRows(raw json.RawMessage) error {
	invalid := errors.New("service metadata version 5 document is invalid")
	var rows []map[string]json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return invalid
	}
	if len(rows) > maxCustomIcons {
		return errors.New("service metadata contains too many custom icons")
	}
	for _, row := range rows {
		if row == nil || len(row) != 2 || !containsOnlyJSONFields(row, "id", "png") {
			return invalid
		}
		for _, key := range []string{"id", "png"} {
			value := bytes.TrimSpace(row[key])
			if len(value) == 0 || value[0] != '"' || len(value) > maxCustomIconStoredBase64+2 {
				return invalid
			}
		}
	}
	return nil
}

// Check the model's quotas, unique IDs and references without decoding images
// while checking an already-validated editor view. parseServiceMetadata checks
// the payload bytes on every file load and serialized candidate.
func indexCustomIcons(d serviceMetadataDocument) (map[string]string, error) {
	invalid := errors.New("service metadata custom icons are invalid")
	if d.Version != serviceMetadataVersionV5 || d.CustomIcons == nil || len(d.CustomIcons) > maxCustomIcons {
		return nil, invalid
	}
	icons := make(map[string]string, len(d.CustomIcons))
	total := 0
	for _, row := range d.CustomIcons {
		if !validCustomIconID(row.ID) || len(row.PNG) > maxCustomIconStoredBase64 {
			return nil, invalid
		}
		if _, exists := icons[row.ID]; exists {
			return nil, invalid
		}
		n := len(row.PNG)
		if n == 0 || n%4 != 0 {
			return nil, invalid
		}
		size := customIconDecodedLen(row.PNG)
		total += size
		if size > maxCustomIconBytes || total > maxCustomIconTotalBytes {
			return nil, invalid
		}
		icons[row.ID] = row.PNG
	}
	for _, row := range d.Services {
		if row.CustomIcon != nil && icons[*row.CustomIcon] == "" {
			return nil, invalid
		}
	}
	return icons, nil
}

// File and candidate parsing call this after checking the document quotas.
func validStoredCustomIcon(row customIconEntry) bool {
	if len(row.PNG) > maxCustomIconStoredBase64 {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(row.PNG)
	return err == nil && len(raw) > 0 && len(raw) <= maxCustomIconBytes &&
		base64.StdEncoding.EncodeToString(raw) == row.PNG && customIconID(raw) == row.ID &&
		validateCanonicalCustomPNG(raw) == nil
}

// Accept only the structure this encoder emits for 8-bit RGB/RGBA: IHDR, one or
// more IDAT, IEND. No ancillary (text, EXIF, ICC, APNG) chunks, no palette, no
// interlace, correct CRCs and no trailing bytes. Then a bounded full decode.
func validateCanonicalCustomPNG(raw []byte) error {
	if len(raw) > maxCustomIconBytes || !bytes.HasPrefix(raw, pngSignature) {
		return errCustomIconInvalid
	}
	rest := raw[len(pngSignature):]
	index, sawIDAT, sawIEND := 0, false, false
	for len(rest) > 0 {
		if sawIEND || len(rest) < 12 {
			return errCustomIconInvalid
		}
		length := binary.BigEndian.Uint32(rest[:4])
		if uint64(length) > uint64(len(rest)-12) {
			return errCustomIconInvalid
		}
		kind := string(rest[4:8])
		data := rest[8 : 8+length]
		if crc32.ChecksumIEEE(rest[4:8+length]) != binary.BigEndian.Uint32(rest[8+length:12+length]) {
			return errCustomIconInvalid
		}
		switch {
		case index == 0:
			// width, height, depth 8, colour type 2|6, compression/filter/interlace 0
			if kind != "IHDR" || length != 13 || data[8] != 8 || data[9] != 2 && data[9] != 6 || data[10] != 0 || data[11] != 0 || data[12] != 0 {
				return errCustomIconInvalid
			}
		case kind == "IDAT":
			sawIDAT = true
		case kind == "IEND" && sawIDAT && length == 0:
			sawIEND = true
		default:
			return errCustomIconInvalid
		}
		rest = rest[12+length:]
		index++
	}
	if !sawIEND {
		return errCustomIconInvalid
	}
	_, err := decodeBoundedPNG(raw)
	return err
}

// DecodeConfig bounds dimensions (and so pixel memory) before the full decode.
func decodeBoundedPNG(raw []byte) (image.Image, error) {
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxCustomIconDimension || config.Height > maxCustomIconDimension {
		return nil, errCustomIconInvalid
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errCustomIconInvalid
	}
	if bounds := img.Bounds(); bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return nil, errCustomIconInvalid
	}
	return img, nil
}

// canonicalCustomIcon turns an untrusted base64 PNG into canonical bytes: the
// encoded length is capped before decoding, pixels are decoded with bounded
// dimensions, normalized to 8-bit non-premultiplied RGBA and re-encoded so no
// source chunk, metadata or animation survives. Pure: no lock, I/O or state.
func canonicalCustomIcon(encoded string) (id, canonicalBase64 string, err error) {
	if encoded == "" || len(encoded) > maxCustomIconInputBase64 {
		return "", "", errCustomIconInvalid
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > maxCustomIconInputBytes || !bytes.HasPrefix(raw, pngSignature) {
		return "", "", errCustomIconInvalid
	}
	src, err := decodeBoundedPNG(raw)
	if err != nil {
		return "", "", err
	}
	bounds := src.Bounds()
	pixels := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(pixels, pixels.Bounds(), src, bounds.Min, draw.Src)
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&out, pixels); err != nil {
		return "", "", errCustomIconInvalid
	}
	canonical := out.Bytes()
	if len(canonical) > maxCustomIconBytes || validateCanonicalCustomPNG(canonical) != nil {
		return "", "", errCustomIconInvalid
	}
	return customIconID(canonical), base64.StdEncoding.EncodeToString(canonical), nil
}

// Explicit opt-in promotion. Legacy or v4 files become v5 only through an
// import/assignment; v5 never downcasts. Validate BEFORE transformation.
func customIconDocumentV5(document serviceMetadataDocument, binding serviceMetadataBinding) (serviceMetadataDocument, error) {
	if err := validateServiceMetadataDocumentModel(document); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(document)
	if !serviceMetadataBoundVersion(out.Version) {
		out.Namespace, out.ControlPlane = binding.Namespace, binding.ControlPlane
		out.Machines = []machineIconEntry{}
		out.Applications = []applicationIconEntry{}
	}
	if out.Version != serviceMetadataVersionV5 {
		out.Version = serviceMetadataVersionV5
		out.CustomIcons = []customIconEntry{}
	}
	if err := validateServiceMetadataBinding(out, binding); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}

// Pure append with explicit quotas. Existing IDs are the caller's idempotent path.
func addCustomIconToDocument(d serviceMetadataDocument, id, canonicalBase64 string) (serviceMetadataDocument, error) {
	if d.Version != serviceMetadataVersionV5 || !validCustomIconID(id) {
		return serviceMetadataDocument{}, errCustomIconInvalid
	}
	total := customIconDecodedLen(canonicalBase64)
	for _, row := range d.CustomIcons {
		if row.ID == id {
			return serviceMetadataDocument{}, errCustomIconInvalid
		}
		total += customIconDecodedLen(row.PNG)
	}
	if len(d.CustomIcons)+1 > maxCustomIcons || total > maxCustomIconTotalBytes {
		return serviceMetadataDocument{}, errCustomIconCapacity
	}
	out := cloneServiceMetadataDocument(d)
	out.CustomIcons = append(out.CustomIcons, customIconEntry{ID: id, PNG: canonicalBase64})
	// Serialization re-runs the strict index and the file-size bounds; a candidate
	// that no longer fits the document is a capacity outcome, never a write.
	if _, err := serializeServiceMetadataDocument(out); err != nil {
		return serviceMetadataDocument{}, errCustomIconCapacity
	}
	return out, nil
}

// Exact decoded length of padded standard base64 (validated elsewhere).
func customIconDecodedLen(encoded string) int {
	n := len(encoded) / 4 * 3
	for i := len(encoded) - 1; i >= 0 && i >= len(encoded)-2 && encoded[i] == '='; i-- {
		n--
	}
	return n
}

// Assign or clear (id == "") one NPM card's custom reference. Name, link, finite
// icon, category and order are preserved; an emptied row is removed.
func mutateServiceMetadataCustomIcon(d serviceMetadataDocument, proxyHostID int, id string) (serviceMetadataDocument, error) {
	if d.Version != serviceMetadataVersionV5 || proxyHostID <= 0 || id != "" && !validCustomIconID(id) {
		return serviceMetadataDocument{}, errCustomIconInvalid
	}
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(d)
	found := false
	for i := range out.Services {
		row := &out.Services[i]
		if row.ProxyHostID != proxyHostID {
			continue
		}
		found = true
		row.CustomIcon = nil
		if id != "" {
			value := id
			row.CustomIcon = &value
		}
		if row.Name == nil && row.URL == nil && row.Category == nil && row.Order == nil && row.Icon == nil && row.CustomIcon == nil && !row.NameNull && !row.URLNull {
			out.Services = append(out.Services[:i], out.Services[i+1:]...)
		}
		break
	}
	if !found && id != "" {
		value := id
		out.Services = append(out.Services, serviceMetadataEntry{ProxyHostID: proxyHostID, CustomIcon: &value})
	}
	if _, err := serializeServiceMetadataDocument(out); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}

// customIconRenderSources returns data: URIs for custom icons referenced by the
// given already-authorized NPM cards only, within the per-response budget. The
// first card to reference an ID pays for it; repeated IDs are charged again
// because each <img> carries its own URI.
func customIconRenderSources(cards []ServiceCard, metadata *ServiceMetadata) []string {
	sources := make([]string, len(cards))
	if metadata == nil || metadata.document.Version != serviceMetadataVersionV5 || len(metadata.CustomIcons) == 0 {
		return sources
	}
	const prefix = "data:image/png;base64,"
	budget := maxCustomIconRenderBytes
	for i, card := range cards {
		if card.PersonalID != "" || card.ID <= 0 || card.CustomIcon == "" {
			continue
		}
		encoded := metadata.CustomIcons[card.CustomIcon]
		if encoded == "" || len(prefix)+len(encoded) > budget {
			continue
		}
		budget -= len(prefix) + len(encoded)
		sources[i] = prefix + encoded
	}
	return sources
}

type mcpCustomIconImportRequest struct {
	PNG      string
	Revision string
}

type customIconTargetForm struct {
	Target     sharedTargetRef `json:"target"`
	Name       string          `json:"name"`
	CustomIcon string          `json:"custom_icon"`
	sharedMutationCAS
}

func (o *SharedIconOperations) customIconsEnabled() bool {
	return serviceMetadataV4RuntimeEnabled && o != nil && o.customIcons
}

// Import stores one canonical image and returns its content ID. It neither
// selects nor reveals a target. The caller must be a current exact shared
// editor through MCP. An already stored identical image returns the same ID
// without a write, even on a stale revision. This lets an authorized editor
// with the exact image distinguish a duplicate from a conflicting new import.
func (o *SharedIconOperations) ImportMCPCustomIcon(r *http.Request, req mcpCustomIconImportRequest) (string, error) {
	if !o.customIconsEnabled() {
		return "", errMetadataStale
	}
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return "", err
	}
	login := identity.Login
	if req.Revision == "" || len(req.Revision) > 256 {
		return "", errMetadataInvalidFields
	}
	// CPU-bound decode happens before any lock, bounded by the MCP slots.
	id, canonical, err := canonicalCustomIcon(req.PNG)
	if err != nil {
		return "", errMetadataInvalidFields
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	if err := o.customIconWriterLocked(r, login); err != nil {
		return "", err
	}
	view := o.editor.view.Load()
	if view.metadata.CustomIcons[id] != "" {
		return id, nil
	}
	if !hmac.Equal([]byte(req.Revision), []byte(view.revision)) {
		return "", errMetadataConflict
	}
	document, err := customIconDocumentV5(view.metadata.document, o.binding)
	if err != nil {
		return "", errMetadataInvalidFields
	}
	document, err = addCustomIconToDocument(document, id, canonical)
	if err != nil {
		if errors.Is(err, errCustomIconCapacity) {
			return "", errCustomIconCapacity
		}
		return "", errMetadataInvalidFields
	}
	err = o.editor.commit(req.Revision, document, serviceMetadataWriteCustomIcon, func() error {
		return o.customIconWriterLocked(r, login)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Caller holds editor.mu. Repeats request admission for the pinned login and the
// held-writer/freshness requirements of every shared MCP operation.
func (o *SharedIconOperations) customIconWriterLocked(r *http.Request, login string) error {
	if !o.customIconsEnabled() {
		return errMetadataStale
	}
	if _, err := o.mcpTargetIdentity(r, login); err != nil {
		return err
	}
	if err := o.checkWriterLocked(); err != nil {
		return err
	}
	if o.editor.directory == nil || o.editor.lock == nil || !o.sharedTargetFresh(o.editor.requestData(o.cache.Get()), o.now()) {
		return errMetadataStale
	}
	return nil
}

// Get returns one currently visible NPM target's custom reference and CAS. Other
// kinds are not supported in this delivery and are indistinguishable from absent.
func (o *SharedIconOperations) GetMCPCustomIcon(r *http.Request, ref sharedTargetRef) (customIconTargetForm, error) {
	if !o.customIconsEnabled() {
		return customIconTargetForm{}, errMetadataStale
	}
	if ref.Kind != sharedTargetNPM {
		return customIconTargetForm{}, errMetadataNotVisible
	}
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return customIconTargetForm{}, err
	}
	login := identity.Login
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	selected, err := o.selectedMCPTargetLocked(r, ref, login, "")
	if err != nil {
		return customIconTargetForm{}, err
	}
	view := o.editor.view.Load()
	diskErr := o.editor.checkDisk(view)
	current, err := o.selectedMCPTargetLocked(r, ref, login, selected.targetRevision)
	if err != nil {
		return customIconTargetForm{}, err
	}
	if diskErr != nil {
		return customIconTargetForm{}, diskErr
	}
	if o.editor.view.Load() != view || current.kind != sharedTargetNPM {
		return customIconTargetForm{}, errMetadataConflict
	}
	custom := view.metadata.Overrides[current.npm.ID].CustomIcon
	if custom != "" && view.metadata.CustomIcons[custom] == "" {
		return customIconTargetForm{}, errMetadataStale
	}
	return customIconTargetForm{Target: current.summary.Target, Name: current.summary.Name, CustomIcon: custom,
		sharedMutationCAS: sharedMutationCAS{Revision: view.revision, TargetRevision: current.targetRevision}}, nil
}

// Set assigns (or, with id == "", clears) a stored custom image on one current
// visible NPM card using document and target CAS. A missing ID fails closed.
// An unchanged assignment is acknowledged without a write.
func (o *SharedIconOperations) SetMCPCustomIcon(r *http.Request, ref sharedTargetRef, cas sharedMutationCAS, id string) error {
	if !o.customIconsEnabled() {
		return errMetadataStale
	}
	if ref.Kind != sharedTargetNPM {
		return errMetadataNotVisible
	}
	if id != "" && !validCustomIconID(id) {
		return errMetadataInvalidFields
	}
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return err
	}
	login := identity.Login
	if err := validateSharedIconMutation(ref, cas, ""); err != nil {
		return err
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	if err := o.customIconWriterLocked(r, login); err != nil {
		return err
	}
	selected, err := o.selectedMCPTargetLocked(r, ref, login, cas.TargetRevision)
	if err != nil {
		return err
	}
	view := o.editor.view.Load()
	if !hmac.Equal([]byte(cas.Revision), []byte(view.revision)) {
		return errMetadataConflict
	}
	if selected.kind != sharedTargetNPM {
		return errMetadataNotVisible
	}
	if id != "" && view.metadata.CustomIcons[id] == "" {
		return errMetadataInvalidFields
	}
	if view.metadata.Overrides[selected.npm.ID].CustomIcon == id {
		return nil
	}
	document, err := customIconDocumentV5(view.metadata.document, o.binding)
	if err != nil {
		return errMetadataInvalidFields
	}
	document, err = mutateServiceMetadataCustomIcon(document, selected.npm.ID, id)
	if err != nil {
		return errMetadataInvalidFields
	}
	return o.editor.commit(cas.Revision, document, serviceMetadataWriteCustomIcon, func() error {
		if err := o.customIconWriterLocked(r, login); err != nil {
			return err
		}
		return o.recheckMCPIconLocked(r, ref, cas, login)
	})
}
