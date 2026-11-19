package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// --- fixtures ---------------------------------------------------------------

func customTestPNG(t *testing.T, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: seed, G: byte(x), B: byte(y), A: 200})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Incompressible pixels, used to approach the per-icon canonical cap.
func customNoisePNG(t *testing.T, side int, seed int64) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	rand.New(rand.NewSource(seed)).Read(img.Pix)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func pngChunk(kind string, data []byte) []byte {
	chunk := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(data)))
	copy(chunk[4:8], kind)
	chunk = append(chunk, data...)
	return binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
}

// Insert an ancillary chunk directly after IHDR (signature 8 + IHDR 25 bytes).
func withAncillary(raw []byte, kind string, data []byte) []byte {
	out := append([]byte{}, raw[:33]...)
	out = append(out, pngChunk(kind, data)...)
	return append(out, raw[33:]...)
}

func customCanonical(t *testing.T, raw []byte) (string, string) {
	t.Helper()
	id, encoded, err := canonicalCustomIcon(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal("valid PNG rejected", err)
	}
	return id, encoded
}

func metadataV5Wire(controlPlane, services, icons string) string {
	return `{"version":5,"namespace":"selected-source","control_plane":"` + controlPlane + `","services":` + services + `,"machines":[],"applications":[],"custom_icons":` + icons + `}`
}

func customIconRows(rows ...[2]string) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = fmt.Sprintf(`{"id":"%s","png":"%s"}`, row[0], row[1])
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// --- canonicalization -------------------------------------------------------

func TestCustomIconCanonicalizationStripsMetadataAndIsDeterministic(t *testing.T) {
	source := withAncillary(customTestPNG(t, 16, 16, 7), "tEXt", []byte("Comment\x00private-operator-note"))
	source = withAncillary(source, "eXIf", []byte("MM\x00*private-exif"))
	id, encoded := customCanonical(t, source)
	again, encodedAgain := customCanonical(t, source)
	if id != again || encoded != encodedAgain {
		t.Fatal("canonicalization is not deterministic")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || bytes.Contains(raw, []byte("private")) || bytes.Contains(raw, []byte("tEXt")) || bytes.Contains(raw, []byte("eXIf")) {
		t.Fatal("ancillary data survived canonicalization")
	}
	if customIconID(raw) != id || !validCustomIconID(id) || validateCanonicalCustomPNG(raw) != nil {
		t.Fatal("content ID or canonical structure mismatch")
	}
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil || decoded.Bounds().Dx() != 16 || decoded.Bounds().Dy() != 16 {
		t.Fatal("canonical pixels not decodable", err)
	}
	if got := color.NRGBAModel.Convert(decoded.At(3, 5)); got != (color.NRGBA{R: 7, G: 3, B: 5, A: 200}) {
		t.Fatal("canonical pixels changed", got)
	}
	// Same pixels from a different source encoding dedupe to the same ID.
	paletted := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.NRGBA{R: 1, G: 2, B: 3, A: 255}, color.NRGBA{R: 4, G: 5, B: 6, A: 255}})
	paletted.SetColorIndex(1, 1, 1)
	direct := image.NewNRGBA(paletted.Bounds())
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			direct.Set(x, y, paletted.At(x, y))
		}
	}
	var a, b bytes.Buffer
	_ = png.Encode(&a, paletted)
	_ = png.Encode(&b, direct)
	idA, _ := customCanonical(t, a.Bytes())
	idB, _ := customCanonical(t, b.Bytes())
	if idA != idB {
		t.Fatal("identical pixels did not produce the same content ID")
	}
}

func TestCustomIconCanonicalizationRejectsUnsafeInput(t *testing.T) {
	valid := customTestPNG(t, 8, 8, 1)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	oversizedRaw := append(append([]byte{}, valid...), make([]byte, maxCustomIconInputBytes)...)
	cases := map[string]string{
		"empty":            "",
		"encoded over cap": strings.Repeat("A", maxCustomIconInputBase64+4),
		"not base64":       "%%%%",
		"unpadded":         strings.TrimRight(base64.StdEncoding.EncodeToString(valid[:10]), "="),
		"raw svg":          base64.StdEncoding.EncodeToString(svg),
		"gif":              base64.StdEncoding.EncodeToString(gif),
		"truncated":        base64.StdEncoding.EncodeToString(valid[:len(valid)/2]),
		"too wide":         base64.StdEncoding.EncodeToString(customTestPNG(t, maxCustomIconDimension+1, 1, 1)),
		"too tall":         base64.StdEncoding.EncodeToString(customTestPNG(t, 1, maxCustomIconDimension+1, 1)),
		"decoded over cap": base64.StdEncoding.EncodeToString(oversizedRaw),
		"large noise":      base64.StdEncoding.EncodeToString(customNoisePNG(t, 64, 1)),
		"url not image":    base64.StdEncoding.EncodeToString([]byte("https://example.test/logo.png")),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if id, encoded, err := canonicalCustomIcon(input); err == nil || id != "" || encoded != "" {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestCustomIconStoredStructureIsStrict(t *testing.T) {
	_, encoded := customCanonical(t, customTestPNG(t, 4, 4, 9))
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	if validateCanonicalCustomPNG(raw) != nil {
		t.Fatal("canonical rejected")
	}
	badCRC := append([]byte{}, raw...)
	badCRC[len(badCRC)-1] ^= 1
	var paletted bytes.Buffer
	_ = png.Encode(&paletted, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}))
	for name, candidate := range map[string][]byte{
		"ancillary": withAncillary(raw, "tEXt", []byte("k\x00v")),
		"bad crc":   badCRC,
		"trailing":  append(append([]byte{}, raw...), 0),
		"palette":   paletted.Bytes(),
		"empty":     {},
	} {
		if validateCanonicalCustomPNG(candidate) == nil {
			t.Fatal(name, "accepted as stored canonical PNG")
		}
	}
}

// --- metadata v5 ------------------------------------------------------------

func TestServiceMetadataV5SchemaAndCompatibility(t *testing.T) {
	id, encoded := customCanonical(t, customTestPNG(t, 8, 8, 2))
	other, otherEncoded := customCanonical(t, customTestPNG(t, 8, 8, 3))
	services := `[{"proxy_host_id":3,"name":"Wiki","icon":"grafana","custom_icon":"` + id + `"}]`
	m, err := parseServiceMetadata([]byte(metadataV5Wire("tailscale", services, customIconRows([2]string{id, encoded}, [2]string{other, otherEncoded}))))
	if err != nil {
		t.Fatal(err)
	}
	if m.Overrides[3].CustomIcon != id || m.Overrides[3].Icon != "grafana" || m.CustomIcons[id] != encoded || len(m.CustomIcons) != 2 {
		t.Fatal("v5 index", m.Overrides[3])
	}
	// Canonical round trip is byte-stable and unreferenced images are retained.
	first, err := serializeServiceMetadataDocument(m.document)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := parseServiceMetadata(first)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := serializeServiceMetadataDocument(reparsed.document)
	if !bytes.Equal(first, second) || reparsed.CustomIcons[other] == "" {
		t.Fatal("v5 encoding not stable")
	}
	bad := map[string]string{
		"v4 with custom_icons":  `{"version":4,"namespace":"selected-source","control_plane":"tailscale","services":[],"machines":[],"applications":[],"custom_icons":[]}`,
		"v4 service reference":  `{"version":4,"namespace":"selected-source","control_plane":"tailscale","services":[{"proxy_host_id":3,"custom_icon":"` + id + `"}],"machines":[],"applications":[]}`,
		"v3 service reference":  `{"version":3,"services":[{"proxy_host_id":3,"custom_icon":"` + id + `"}]}`,
		"v5 missing array":      `{"version":5,"namespace":"selected-source","control_plane":"tailscale","services":[],"machines":[],"applications":[]}`,
		"v5 null array":         metadataV5Wire("tailscale", "[]", "null"),
		"dangling reference":    metadataV5Wire("tailscale", services, "[]"),
		"null reference":        metadataV5Wire("tailscale", `[{"proxy_host_id":3,"custom_icon":null}]`, "[]"),
		"id mismatch":           metadataV5Wire("tailscale", "[]", customIconRows([2]string{other, encoded})),
		"duplicate":             metadataV5Wire("tailscale", "[]", customIconRows([2]string{id, encoded}, [2]string{id, encoded})),
		"unpadded base64":       metadataV5Wire("tailscale", "[]", customIconRows([2]string{id, strings.TrimRight(encoded, "=") + "x"})),
		"extra row key":         metadataV5Wire("tailscale", "[]", `[{"id":"`+id+`","png":"`+encoded+`","label":"x"}]`),
		"case variant row key":  metadataV5Wire("tailscale", "[]", `[{"ID":"`+id+`","png":"`+encoded+`"}]`),
		"headscale with device": strings.Replace(metadataV5Wire("headscale", "[]", "[]"), `"machines":[]`, `"machines":[{"device_id_type":"id","device_id":"x","icon":"generic"}]`, 1),
	}
	for name, document := range bad {
		t.Run(name, func(t *testing.T) { requireMetadataInvalid(t, document) })
	}
	if _, err := parseServiceMetadata([]byte(metadataV5Wire("headscale", services, customIconRows([2]string{id, encoded})))); err != nil {
		t.Fatal("headscale NPM custom icons rejected", err)
	}
	// Legacy writers keep rejecting a v5-bearing document instead of dropping data.
	if _, err := serializeServiceMetadataDocumentV3(m.document); err == nil {
		t.Fatal("v3 encoder accepted v5")
	}
}

func TestServiceMetadataV5LegacyEditsPreserveCustomReference(t *testing.T) {
	id, encoded := customCanonical(t, customTestPNG(t, 8, 8, 4))
	source := metadataV5Wire("tailscale", `[{"proxy_host_id":3,"name":"Wiki","category":"Tools","order":2,"custom_icon":"`+id+`"},{"proxy_host_id":7,"name":"Other"}]`, customIconRows([2]string{id, encoded}))
	m, err := parseServiceMetadata([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, reset := range []bool{false, true} {
		updated, err := mutateServiceMetadata(m.document, 3, "Renamed", "", "generic", reset)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Version != serviceMetadataVersionV5 {
			t.Fatal("legacy dialog changed version")
		}
		for _, row := range updated.Services {
			if row.ProxyHostID == 3 && (row.CustomIcon == nil || *row.CustomIcon != id || row.Category == nil || *row.Category != "Tools") {
				t.Fatal("legacy dialog stripped custom/category", reset)
			}
		}
	}
	finite, err := mutateServiceMetadataIcon(m.document, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if finite.Version != serviceMetadataVersionV5 || finite.Services[0].CustomIcon == nil {
		t.Fatal("finite icon reset stripped custom reference")
	}
	cleared, err := mutateServiceMetadataCustomIcon(m.document, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.CustomIcons) != 1 || cleared.Services[0].CustomIcon != nil || cleared.Services[0].Name == nil {
		t.Fatal("clearing must keep image and other fields")
	}
	m, err = parseServiceMetadata([]byte(metadataV5Wire("tailscale", `[{"proxy_host_id":9,"custom_icon":"`+id+`"}]`, customIconRows([2]string{id, encoded}))))
	if err != nil {
		t.Fatal(err)
	}
	if cleared, err = mutateServiceMetadataCustomIcon(m.document, 9, ""); err != nil || len(cleared.Services) != 0 {
		t.Fatal("emptied row retained", err)
	}
	if _, err := mutateServiceMetadataCustomIcon(m.document, 9, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing ID assigned")
	}
}

func TestServiceMetadataV5Quotas(t *testing.T) {
	binding := serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "portal-demo-01"}
	d, err := customIconDocumentV5(metadataV4(t).document, binding)
	if err != nil || d.Version != serviceMetadataVersionV5 || d.CustomIcons == nil || len(d.Machines) != 2 || len(d.Applications) != 2 {
		t.Fatal("v4 promotion must be lossless", err)
	}
	// Aggregate bytes: near-cap images exceed the incumbent 256 KiB file size,
	// which v5 permits for images only, until the explicit aggregate cap.
	stored, total := 0, 0
	for seed := int64(1); seed <= maxCustomIcons; seed++ {
		id, encoded, err := canonicalCustomIcon(base64.StdEncoding.EncodeToString(customNoisePNG(t, 44, seed)))
		if err != nil {
			t.Fatal("near-cap fixture rejected", err)
		}
		next, err := addCustomIconToDocument(d, id, encoded)
		if errors.Is(err, errCustomIconCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		d, stored, total = next, stored+1, total+customIconDecodedLen(encoded)
	}
	if stored == 0 || stored >= maxCustomIcons || total > maxCustomIconTotalBytes || total <= maxServiceMetadataBytes {
		t.Fatal("aggregate quota", stored, total)
	}
	data, err := serializeServiceMetadataDocument(d)
	if err != nil || len(data) <= maxServiceMetadataBytes || len(data) > maxServiceMetadataReadBytes {
		t.Fatal("large v5 document", err, len(data))
	}
	if _, err := parseServiceMetadata(data); err != nil {
		t.Fatal(err)
	}
	// A non-v5 document never gains the larger budget.
	requireMetadataInvalid(t, `{"version":3,"services":[]}`+strings.Repeat(" ", maxServiceMetadataBytes))
	// Count quota with tiny distinct images.
	d, _ = customIconDocumentV5(metadataV4(t).document, binding)
	for i := 0; i <= maxCustomIcons; i++ {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		img.Pix = []byte{byte(i), byte(i >> 8), 1, 255}
		var raw bytes.Buffer
		_ = png.Encode(&raw, img)
		id, encoded := customCanonical(t, raw.Bytes())
		next, err := addCustomIconToDocument(d, id, encoded)
		if i == maxCustomIcons {
			if !errors.Is(err, errCustomIconCapacity) {
				t.Fatal("count quota not enforced", err)
			}
			break
		}
		if err != nil {
			t.Fatal(i, err)
		}
		d = next
	}
}

func TestCustomIconFifthCardPreservesFourBundledAssignments(t *testing.T) {
	const source = `{"version":4,"namespace":"selected-source","control_plane":"tailscale","services":[{"proxy_host_id":1,"icon":"paperless-ngx"},{"proxy_host_id":2,"icon":"immich"},{"proxy_host_id":3,"icon":"immich"},{"proxy_host_id":4,"icon":"nextcloud"}],"machines":[],"applications":[]}`
	prior, err := parseServiceMetadata([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	d, err := customIconDocumentV5(prior.document, serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "selected-source"})
	if err != nil {
		t.Fatal(err)
	}
	id, encoded := customCanonical(t, customTestPNG(t, 8, 8, 1))
	d, err = addCustomIconToDocument(d, id, encoded)
	if err != nil {
		t.Fatal(err)
	}
	d, err = mutateServiceMetadataCustomIcon(d, 5, id)
	if err != nil {
		t.Fatal(err)
	}
	// The preexisting shared dialog knows nothing about custom PNGs. Its reset
	// on the new card must not clear the custom reference or downgrade the file.
	d, err = mutateServiceMetadata(d, 5, "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := serializeServiceMetadataDocument(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseServiceMetadata(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Overrides) != 5 || got.document.Version != serviceMetadataVersionV5 || got.Overrides[5].CustomIcon != id || got.CustomIcons[id] != encoded {
		t.Fatal("custom logo lost in shared reset or reload")
	}
	for card, before := range prior.Overrides {
		if after := got.Overrides[card]; after.Icon != before.Icon || after.CustomIcon != "" {
			t.Fatalf("existing bundled logo changed on card %d", card)
		}
	}
}

// --- guarded operations -----------------------------------------------------

func customIconOps(t *testing.T) (*SharedIconOperations, *http.Request, sharedTargetRef) {
	t.Helper()
	o, r, ref, _ := sharedTargetFixture(t, sharedTargetNPM)
	o.customIcons = true
	return o, r, ref
}

func TestCustomIconOperationsLifecycle(t *testing.T) {
	o, r, ref := customIconOps(t)
	form, err := o.GetMCPCustomIcon(r, ref)
	if err != nil || form.CustomIcon != "" || form.Revision == "" || form.TargetRevision == "" {
		t.Fatal("initial get", err)
	}
	source := base64.StdEncoding.EncodeToString(customTestPNG(t, 16, 16, 5))
	before := o.editor.view.Load()
	if _, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: "stale"}); !errors.Is(err, errMetadataConflict) {
		t.Fatal("stale import", err)
	}
	sharedNPMUnchanged(t, o.editor, before, before.bytes)
	id, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision})
	if err != nil || !validCustomIconID(id) {
		t.Fatal("import", err)
	}
	disk, _ := os.ReadFile(o.editor.path)
	if !bytes.Contains(disk, []byte(`"version": 5`)) || !bytes.Contains(disk, []byte(`"proxy_host_id": 3`)) || !bytes.Contains(disk, []byte(`"device_id": "node-1"`)) {
		t.Fatal("promotion lost existing entries", string(disk))
	}
	// Import alone assigns nothing and grants no visibility.
	if current, _ := o.GetMCPCustomIcon(r, ref); current.CustomIcon != "" {
		t.Fatal("import assigned an icon")
	}
	// Idempotent retry with the old CAS: same ID, no write.
	after := o.editor.view.Load()
	if again, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision}); err != nil || again != id {
		t.Fatal("dedup", err)
	}
	sharedNPMUnchanged(t, o.editor, after, after.bytes)

	form, _ = o.GetMCPCustomIcon(r, ref)
	if err := o.SetMCPCustomIcon(r, ref, form.sharedMutationCAS, "sha256:"+strings.Repeat("a", 64)); !errors.Is(err, errMetadataInvalidFields) {
		t.Fatal("missing ID", err)
	}
	if err := o.SetMCPCustomIcon(r, ref, sharedMutationCAS{Revision: form.Revision, TargetRevision: "old"}, id); !errors.Is(err, errMetadataConflict) {
		t.Fatal("target CAS", err)
	}
	if err := o.SetMCPCustomIcon(r, ref, form.sharedMutationCAS, id); err != nil {
		t.Fatal("assign", err)
	}
	assigned, err := o.GetMCPCustomIcon(r, ref)
	if err != nil || assigned.CustomIcon != id || assigned.TargetRevision == form.TargetRevision {
		t.Fatal("read back", err)
	}
	if o.editor.view.Load().metadata.Overrides[3].Icon != "grafana" {
		t.Fatal("finite icon not preserved")
	}
	// Old CAS after a committed change conflicts rather than replaying.
	if err := o.SetMCPCustomIcon(r, ref, form.sharedMutationCAS, ""); !errors.Is(err, errMetadataConflict) {
		t.Fatal("replay", err)
	}
	if err := o.SetMCPCustomIcon(r, ref, assigned.sharedMutationCAS, ""); err != nil {
		t.Fatal("clear", err)
	}
	cleared, _ := o.GetMCPCustomIcon(r, ref)
	if cleared.CustomIcon != "" || o.editor.view.Load().metadata.CustomIcons[id] == "" {
		t.Fatal("clear must keep the stored image")
	}
	// Restart: a fresh editor on the written file validates and loads v5.
	path, config := o.editor.path, o.editor.config
	if err := o.editor.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newServiceMetadataEditor(path, config)
	if err != nil || restarted.view.Load().metadata.CustomIcons[id] == "" {
		t.Fatal("restart did not load stored image", err)
	}
	_ = restarted.Close()
}

func TestCustomIconOperationsAuthority(t *testing.T) {
	o, r, ref := customIconOps(t)
	form, _ := o.GetMCPCustomIcon(r, ref)
	source := base64.StdEncoding.EncodeToString(customTestPNG(t, 4, 4, 1))
	before := o.editor.view.Load()

	nonEditor := sharedTargetRequest(t, o, "carol@example.com")
	if _, err := o.ImportMCPCustomIcon(nonEditor, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision}); !errors.Is(err, errMetadataForbidden) {
		t.Fatal("non-editor import", err)
	}
	if _, err := o.GetMCPCustomIcon(nonEditor, ref); !errors.Is(err, errMetadataForbidden) {
		t.Fatal("non-editor get", err)
	}
	// A valid editor cannot use another identity's opaque handle.
	bob := sharedTargetRequest(t, o, "bob@example.com")
	if err := o.SetMCPCustomIcon(bob, ref, form.sharedMutationCAS, ""); !errors.Is(err, errMetadataNotVisible) {
		t.Fatal("cross-identity handle", err)
	}
	machine := o.sharedTargetHandle("alice@example.com", sharedTargetMachine, "id", "node-1")
	if _, err := o.GetMCPCustomIcon(r, machine); !errors.Is(err, errMetadataNotVisible) {
		t.Fatal("machine target supported in this delivery", err)
	}
	if err := o.SetMCPCustomIcon(r, machine, form.sharedMutationCAS, ""); !errors.Is(err, errMetadataNotVisible) {
		t.Fatal("machine target set", err)
	}
	o.customIcons = false
	if _, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision}); !errors.Is(err, errMetadataStale) {
		t.Fatal("disabled import", err)
	}
	o.customIcons = true
	// Stale snapshot fails closed.
	now := o.now
	o.now = func() time.Time { return now().Add(time.Hour) }
	if _, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision}); err == nil {
		t.Fatal("stale snapshot import")
	}
	sharedNPMUnchanged(t, o.editor, before, before.bytes)
	// A disabled (uncertain) writer refuses; nothing is retried or repaired.
	o.now = now
	o.editor.disabled = true
	if _, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: source, Revision: form.Revision}); err == nil {
		t.Fatal("disabled writer import")
	}
	sharedNPMUnchanged(t, o.editor, before, before.bytes)
}

func TestCustomIconCommitNeverDowngrades(t *testing.T) {
	o, r, ref := customIconOps(t)
	form, _ := o.GetMCPCustomIcon(r, ref)
	if _, err := o.ImportMCPCustomIcon(r, mcpCustomIconImportRequest{PNG: base64.StdEncoding.EncodeToString(customTestPNG(t, 2, 2, 1)), Revision: form.Revision}); err != nil {
		t.Fatal(err)
	}
	view := o.editor.view.Load()
	v4 := cloneServiceMetadataDocument(view.metadata.document)
	v4.Version, v4.CustomIcons = serviceMetadataVersionV4, nil
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	for _, kind := range []serviceMetadataWriteKind{serviceMetadataWriteNPM, serviceMetadataWriteIcon, serviceMetadataWriteCustomIcon} {
		if err := o.editor.commit(view.revision, v4, kind, func() error { return nil }); !errors.Is(err, errMetadataStorage) {
			t.Fatal("v5 downgraded", kind, err)
		}
	}
}

func TestCustomIconOrdinaryWritesCannotPromote(t *testing.T) {
	o, _, _ := customIconOps(t) // v4 source
	view := o.editor.view.Load()
	v5, err := customIconDocumentV5(view.metadata.document, o.binding)
	if err != nil {
		t.Fatal(err)
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	for _, kind := range []serviceMetadataWriteKind{serviceMetadataWriteNPM, serviceMetadataWriteIcon} {
		if err := o.editor.commit(view.revision, v5, kind, func() error { return nil }); !errors.Is(err, errMetadataStorage) {
			t.Fatal("non-custom write promoted to v5", kind, err)
		}
	}
	sharedNPMUnchanged(t, o.editor, view, view.bytes)
}

// --- portal rendering -------------------------------------------------------

func TestCustomIconPortalRenderIsAuthorizationFiltered(t *testing.T) {
	id, encoded := customCanonical(t, customTestPNG(t, 8, 8, 6))
	m, err := parseServiceMetadata([]byte(metadataV5Wire("headscale", `[{"proxy_host_id":1,"icon":"grafana","custom_icon":"`+id+`"}]`, customIconRows([2]string{id, encoded}))))
	if err != nil {
		t.Fatal(err)
	}
	data := standardTestData()
	data.ControlPlane.Provider = controlPlaneHeadscale
	data.ServiceMetadata = m
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8")
	render := func(enabled bool, login string) string {
		portal := NewPortalHandler(newTestCache(data))
		portal.metadataBinding = serviceMetadataBinding{ControlPlane: controlPlaneHeadscale, Namespace: "selected-source"}
		portal.customIcons = enabled
		return doPortalRequest(IdentityMiddleware(trusted, portal), "127.0.0.1:1234", login).Body.String()
	}
	alice := render(true, "alice@example.com")
	if !strings.Contains(alice, "data:image/png;base64,"+encoded) || !strings.Contains(alice, "data-custom-icon") || strings.Contains(alice, id) {
		t.Fatal("authorized card lacks custom icon or exposes its ID")
	}
	// Bob cannot see proxy host 1: no bytes or ID anywhere in the response.
	if bob := render(true, "bob@example.com"); strings.Contains(bob, encoded) || strings.Contains(bob, id) || strings.Contains(bob, "data:image/png") {
		t.Fatal("custom image leaked to unauthorized identity")
	}
	// Disabled: finite icon only, exactly as before.
	if off := render(false, "alice@example.com"); strings.Contains(off, "data:image/png") || !strings.Contains(off, "/static/service-icons/grafana.png") {
		t.Fatal("disabled render changed")
	}
}

func TestCustomIconRenderBudget(t *testing.T) {
	id := "sha256:" + strings.Repeat("1", 64)
	metadata := &ServiceMetadata{document: serviceMetadataDocument{Version: serviceMetadataVersionV5}, CustomIcons: map[string]string{id: strings.Repeat("A", maxCustomIconStoredBase64)}}
	cards := make([]ServiceCard, 200)
	for i := range cards {
		cards[i] = ServiceCard{ID: i + 1, CustomIcon: id}
	}
	cards[0].PersonalID, cards[0].ID = "ps_x", 0
	total := 0
	for i, src := range customIconRenderSources(cards, metadata) {
		if i == 0 && src != "" {
			t.Fatal("personal card received shared custom icon")
		}
		total += len(src)
	}
	if total == 0 || total > maxCustomIconRenderBytes {
		t.Fatal("render budget", total)
	}
	v4 := &ServiceMetadata{document: serviceMetadataDocument{Version: serviceMetadataVersionV4}, CustomIcons: metadata.CustomIcons}
	for _, src := range customIconRenderSources(cards, v4) {
		if src != "" {
			t.Fatal("non-v5 rendered custom icon")
		}
	}
}

// --- MCP surface ------------------------------------------------------------

func customIconTools(t *testing.T, enabled bool) (*mcpApplicationTools, *http.Request, sharedTargetRef) {
	t.Helper()
	o, r, ref := customIconOps(t)
	o.customIcons = enabled
	cfg := &Config{PortalEditMode: serviceEditShared, ControlPlane: controlPlaneTailscale, StateNamespace: o.binding.Namespace,
		TrustedProxyCIDR: o.trusted, PollInterval: 30 * time.Second, MCP: o.mcp, CustomIcons: enabled}
	tools := newMCPApplicationTools(cfg, o, o.cache, nil)
	if tools.shared != o {
		t.Fatal("fixture did not bind shared operations")
	}
	tools.now = o.now
	return tools, r, ref
}

func TestCustomIconMCPDisabledSurfaceUnchanged(t *testing.T) {
	disabled, r, _ := customIconTools(t, false)
	listed, err := disabled.List(r)
	if err != nil {
		t.Fatal(err)
	}
	names := s4hNames(t, listed)
	if strings.Contains(strings.Join(names, ","), "custom_icon") || strings.Join(names, ",") != "icons_list,shared_targets_list,shared_target_get,shared_icon_set,shared_icon_reset" {
		t.Fatal("disabled discovery changed", names)
	}
	for _, name := range mcpCustomIconToolNames {
		if _, err := disabled.Call(r, name, json.RawMessage(`{}`)); !errors.Is(err, errMCPInvalidParams) {
			t.Fatal("disabled tool callable", name)
		}
	}
}

func TestCustomIconMCPEnabledFlow(t *testing.T) {
	enabled, r, ref := customIconTools(t, true)
	listed, err := enabled.List(r)
	if err != nil {
		t.Fatal(err)
	}
	names := s4hNames(t, listed)
	if len(names) < 3 || strings.Join(names[len(names)-3:], ",") != "custom_icon_import,custom_icon_get,custom_icon_set" {
		t.Fatal("enabled discovery", names)
	}
	targetArg := map[string]any{"target": map[string]string{"kind": string(ref.Kind), "handle": ref.Handle}}
	result, err := enabled.Call(r, "custom_icon_get", s4hJSON(t, targetArg))
	if err != nil || result.IsError {
		t.Fatal("enabled get", err, string(s4hStructured(t, result)))
	}
	var form customIconTargetForm
	_ = json.Unmarshal(s4hStructured(t, result), &form)
	source := base64.StdEncoding.EncodeToString(customTestPNG(t, 12, 12, 8))
	result, _ = enabled.Call(r, "custom_icon_import", s4hJSON(t, map[string]string{"png_base64": source, "revision": form.Revision}))
	var imported map[string]string
	_ = json.Unmarshal(s4hStructured(t, result), &imported)
	if result.IsError || imported["status"] != "saved" || !validCustomIconID(imported["custom_icon"]) {
		t.Fatal("import via MCP", string(s4hStructured(t, result)))
	}
	result, _ = enabled.Call(r, "custom_icon_get", s4hJSON(t, targetArg))
	_ = json.Unmarshal(s4hStructured(t, result), &form)
	set := map[string]any{"target": targetArg["target"], "revision": form.Revision, "target_revision": form.TargetRevision, "custom_icon": imported["custom_icon"]}
	if result, _ = enabled.Call(r, "custom_icon_set", s4hJSON(t, set)); result.IsError {
		t.Fatal("set via MCP", string(s4hStructured(t, result)))
	}
	result, _ = enabled.Call(r, "custom_icon_get", s4hJSON(t, targetArg))
	_ = json.Unmarshal(s4hStructured(t, result), &form)
	if form.CustomIcon != imported["custom_icon"] {
		t.Fatal("read back via MCP")
	}
	// Strict arguments: encoded cap before decoding, ID syntax, exact keys.
	for name, args := range map[string]any{
		"custom_icon_import": map[string]string{"png_base64": strings.Repeat("A", maxCustomIconInputBase64+1), "revision": "r"},
		"custom_icon_set":    map[string]any{"target": targetArg["target"], "revision": "r", "target_revision": "t", "custom_icon": "grafana"},
		"custom_icon_get":    map[string]any{"target": targetArg["target"], "url": "https://example.test/logo.png"},
	} {
		if _, err := enabled.Call(r, name, s4hJSON(t, args)); !errors.Is(err, errMCPInvalidParams) {
			t.Fatal("strict arguments", name)
		}
	}
	// A maximal import request still fits the unchanged 16 KiB MCP request limit.
	maxRequest := s4hJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "custom_icon_import",
		"arguments": map[string]string{"png_base64": strings.Repeat("A", maxCustomIconInputBase64), "revision": strings.Repeat("r", 256)}}})
	if len(maxRequest) > mcpMaxRequestBytes {
		t.Fatal("maximal import exceeds MCP request limit", len(maxRequest))
	}
	for _, name := range mcpCustomIconToolNames {
		d := mcpApplicationTool(name)
		if d == nil || d["outputSchema"] == nil || d["description"] == "" {
			t.Fatal("descriptor", name)
		}
		if d["annotations"].(map[string]bool)["readOnlyHint"] != (name == "custom_icon_get") {
			t.Fatal("annotations", name)
		}
	}
	if mcpApplicationErrorCode(errCustomIconCapacity) != "capacity" {
		t.Fatal("capacity code")
	}
}

// --- configuration ----------------------------------------------------------

func TestCustomIconsConfig(t *testing.T) {
	lookup := func(values map[string]string) configLookup {
		return func(key string) (string, bool, error) { v, ok := values[key]; return v, ok, nil }
	}
	shared := editorTestConfig()
	mcp := &MCPConfig{Origin: shared.Origin, Host: shared.Host}
	if on, err := loadCustomIconsConfig(lookup(nil), serviceEditShared, shared, mcp, "ns"); on || err != nil {
		t.Fatal("absent must be off")
	}
	for _, value := range []string{"", "true", "on", "Enabled", " enabled"} {
		if _, err := loadCustomIconsConfig(lookup(map[string]string{"PORTAL_CUSTOM_ICONS": value}), serviceEditShared, shared, mcp, "ns"); err == nil {
			t.Fatal("non-canonical value accepted", value)
		}
	}
	enabled := lookup(map[string]string{"PORTAL_CUSTOM_ICONS": "enabled"})
	for name, check := range map[string]func() (bool, error){
		"browser mode": func() (bool, error) { return loadCustomIconsConfig(enabled, serviceEditBrowser, nil, mcp, "ns") },
		"no editor":    func() (bool, error) { return loadCustomIconsConfig(enabled, serviceEditShared, nil, mcp, "ns") },
		"no mcp":       func() (bool, error) { return loadCustomIconsConfig(enabled, serviceEditShared, shared, nil, "ns") },
		"no namespace": func() (bool, error) { return loadCustomIconsConfig(enabled, serviceEditShared, shared, mcp, "") },
	} {
		if on, err := check(); on || err == nil {
			t.Fatal("missing prerequisite accepted", name)
		}
	}
	if on, err := loadCustomIconsConfig(enabled, serviceEditShared, shared, mcp, "ns"); !on || err != nil {
		t.Fatal("complete opt-in rejected", err)
	}
}

func TestCustomIconsStartupFailsClosed(t *testing.T) {
	id, encoded := customCanonical(t, customTestPNG(t, 8, 8, 1))
	config := editorTestConfig()
	config.Binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "selected-source"}
	valid := metadataV5Wire("tailscale", `[{"proxy_host_id":3,"custom_icon":"`+id+`"}]`, customIconRows([2]string{id, encoded}))
	e, err := newServiceMetadataEditor(editorTestFile(t, valid), config)
	if err != nil {
		t.Fatal("valid v5 startup", err)
	}
	_ = e.Close()
	dangling := metadataV5Wire("tailscale", `[{"proxy_host_id":3,"custom_icon":"`+id+`"}]`, "[]")
	path := editorTestFile(t, dangling)
	if e, err := newServiceMetadataEditor(path, config); err == nil || e != nil {
		t.Fatal("dangling reference started")
	}
	if disk, _ := os.ReadFile(path); string(disk) != dangling {
		t.Fatal("startup repaired or rewrote file")
	}
	if err := preflightServiceMetadataBinding(path, config.Binding); err == nil {
		t.Fatal("preflight accepted invalid v5")
	}
}
