package orchestrator

import (
	"errors"
	"fmt"
	"sort"
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
// The server mirrors that rule byte for byte instead of inventing a second
// interpretation, so a non-browser client cannot smuggle in a patch the console
// would have refused, and an input accepted by one side is accepted by both.
func validatePatchImageOwnership(patch *structpb.Struct, images []store.BundleImage) error {
	if path := nonBundleImagePath(patch, images); path != "" {
		return &nonBundleImageError{path: path}
	}
	return nil
}

// nonBundleImagePath returns the first patch path that references an image
// outside the bundle, or "" when every image path is owned by it. Paths are
// visited in sorted order so the reported path does not depend on Go's map
// iteration.
func nonBundleImagePath(patch *structpb.Struct, images []store.BundleImage) string {
	if patch == nil || len(patch.Fields) == 0 {
		return ""
	}
	paths := make([]string, 0, len(patch.Fields))
	for path := range patch.Fields {
		paths = append(paths, path)
	}
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
