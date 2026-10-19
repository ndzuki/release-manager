package orchestrator

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ndzuki/release-manager/internal/store"
)

// nonBundleImageError reports a values patch that references an image outside
// the bundle's declared image set (AC-056-01 / REQ-056).
type nonBundleImageError struct {
	path string
}

func (e *nonBundleImageError) Error() string {
	return fmt.Sprintf("non_bundle_image: patch path %q is not a Bundle image values path", e.path)
}

// validatePatchImageOwnership applies the server half of AC-056-01: a patch may
// only touch image paths that belong to the bundle being deployed.
//
// REQ-056 requires a DOUBLE rejection ("前端+后端双重拒绝"), and the console half
// already exists (web/src/stores/operationForm.ts:266 rejects a patch path that
// mentions "image" but is not under one of the bundle's image values paths).
// The server mirrors that path rule byte for byte instead of inventing a second
// interpretation. A patch is a JSON Merge Patch, and RFC 7386 merges nested
// objects recursively, so the rule is applied to every object path the merge can
// reach, not just the top-level keys: `{"sidecar":{"image":{"tag":"v2"}}}` writes
// the same value as the console's dotted path `sidecar.image.tag`, which the
// console refuses.
//
// Arrays are expanded by index for the same reason: RFC 7386 replaces an array
// wholesale, but the replacement becomes effective values verbatim, so
// `{"sidecars":[{"image":{"tag":"evil"}}]}` writes `sidecars.0.image` just as
// surely as the object form writes `sidecar.image`. The console cannot build a
// patch path containing `[`/`]` (operationForm.ts), so this shape is API-only,
// which makes the server-side check the only line of defence.
func validatePatchImageOwnership(patch *structpb.Struct, images []store.BundleImage) error {
	if path := nonBundleImagePath(patch, images); path != "" {
		return &nonBundleImageError{path: path}
	}
	return nil
}

// nonBundleImagePath returns the first patch path (including every intermediate
// object path and every index-expanded array element path) that references an
// image outside the bundle, or "" when every image path is owned by it. Paths
// are visited in sorted order so the reported path does not depend on Go's map
// iteration.
func nonBundleImagePath(patch *structpb.Struct, images []store.BundleImage) string {
	if patch == nil || len(patch.Fields) == 0 {
		return ""
	}
	paths := make([]string, 0, len(patch.Fields))
	collectPatchPaths("", patch.Fields, &paths)
	sort.Strings(paths)
	for _, path := range paths {
		// The console decides "is this path about an image?" case-insensitively
		// and "is it inside the bundle?" byte-exactly. Both halves matter: the
		// rule must not silently accept `Image.Tag` when the bundle declares
		// `image.tag`, because the frontend refuses that input.
		if !strings.Contains(strings.ToLower(path), "image") {
			continue
		}
		if !patchPathIsBundleImage(path, images) {
			return path
		}
	}
	return ""
}

// collectPatchPaths appends every path a JSON Merge Patch can write, joining
// nested object keys with "." and expanding array elements with their index
// (`sidecars.0.image`). RFC 7386 merges object values recursively and replaces
// every other value (scalar, null, array) wholesale, so each object path is
// itself writable. Arrays are therefore writable as a whole, and their contents
// land in the effective values verbatim, so each expanded element path is
// checked with the same rule as an object path. The dotted join matches the
// console's path syntax and the bundle image values paths
// (store.BundleImage.ValuesPath).
func collectPatchPaths(prefix string, fields map[string]*structpb.Value, paths *[]string) {
	for key, value := range fields {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		*paths = append(*paths, path)
		collectPatchValuePaths(path, value, paths)
	}
}

// collectPatchValuePaths descends into the value a merge patch carries: an
// object contributes its keys, an array contributes one indexed path per element
// plus that element's descendants, and scalars are leaves. Scalar array elements
// are still expanded so the traversal is uniform; paths that do not mention
// "image" are ignored by the caller, so `{"x":[1,2,3]}` and `{"sidecars":[]}`
// stay accepted and cannot panic.
func collectPatchValuePaths(path string, value *structpb.Value, paths *[]string) {
	if nested := value.GetStructValue(); nested != nil {
		collectPatchPaths(path, nested.GetFields(), paths)
		return
	}
	if list := value.GetListValue(); list != nil {
		for index, element := range list.GetValues() {
			elementPath := path + "." + strconv.Itoa(index)
			*paths = append(*paths, elementPath)
			collectPatchValuePaths(elementPath, element, paths)
		}
	}
}

// patchPathIsBundleImage reports whether path IS one of the bundle image values
// paths or lives underneath one. It mirrors operationForm.ts:266, which allows
// `valuesPath` exactly and any `valuesPath.`-prefixed descendant.
func patchPathIsBundleImage(path string, images []store.BundleImage) bool {
	for _, image := range images {
		if image.ValuesPath == "" {
			continue
		}
		if path == image.ValuesPath || strings.HasPrefix(path, image.ValuesPath+".") {
			return true
		}
	}
	return false
}

// prepareValuesError maps a values-preparation failure onto the wire contract.
// invalid_argument remains the default; the non_bundle_image violation keeps
// its stable X-Reason-Code so the console can branch on it (AC-056-01).
func prepareValuesError(err error) error {
	var ownership *nonBundleImageError
	if errors.As(err, &ownership) {
		connectErr := connect.NewError(connect.CodeInvalidArgument, err)
		connectErr.Meta().Set("X-Reason-Code", "non_bundle_image")
		return connectErr
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}
