package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedBundleWithImages creates a validated bundle declaring the given image
// values paths and makes it def-001's current bundle, so both the reachability
// gate and the image-ownership check see it.
func seedBundleWithImages(t *testing.T, st store.Store, id string, images []store.BundleImage) {
	t.Helper()
	digest := fmt.Sprintf("%064x", 0x5b)
	require.NoError(t, st.Bundles().Create(context.Background(), &store.ReleaseBundle{
		ID: id, Name: "bundle with images", DigestAlg: "sha256", DigestValue: digest,
		Status: store.BundleValidated, ChartRef: "nginx", ChartDigest: "sha256:" + digest,
		Images: images, CreatedAt: time.Now().UTC(),
	}))
	linkCurrentBundle(t, st, "def-001", id)
}

// imageBundleImages is one bundle image bound to the Helm value image.tag.
func imageBundleImages() []store.BundleImage {
	return []store.BundleImage{{
		Ref:        "registry.example/team/api:v1",
		Digest:     fmt.Sprintf("sha256:%064x", 0x1a),
		ValuesPath: "image.tag",
		ValueKind:  store.ImageValueTag,
	}}
}

func imagePatch(path, value string) *structpb.Struct {
	return &structpb.Struct{Fields: map[string]*structpb.Value{path: structpb.NewStringValue(value)}}
}

func createOperationWithPatch(bundleID string, patch *structpb.Struct, key string) *connect.Request[orchestratorv1.CreateOperationRequest] {
	return withIdempotencyKey(connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            bundleID,
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		ValuesPatch:         patch,
	}), key)
}

// AC-056-01 (TASK-325 W2): CreateOperation refuses a patch that references an
// image outside the bundle, with the stable X-Reason-Code the console already
// maps (web/src/connect/operation-api.ts:374), and writes nothing.
func TestCreateOperation_RejectsNonBundleImagePatch(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedBundleWithImages(t, st, "bundle-images", imageBundleImages())

	_, err := svc.CreateOperation(adminCtx(),
		createOperationWithPatch("bundle-images", imagePatch("sidecar.image.tag", "v2"), "idem-non-bundle-image"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "non_bundle_image")

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "non_bundle_image", connectErr.Meta().Get("X-Reason-Code"))

	operations, listErr := st.Operations().List(context.Background(), "def-001")
	require.NoError(t, listErr)
	assert.Empty(t, operations, "a rejected patch must not create an operation")
}

// AC-056-01: the gate only fires on image paths that are outside the bundle.
// The accepted cases are the negative control: a bundle-owned image path, a
// descendant of one, and a patch that does not mention images at all.
func TestCreateOperation_ImagePatchOwnership(t *testing.T) {
	testCases := []struct {
		name    string
		patch   *structpb.Struct
		wantErr string
	}{
		{name: "bundle image path", patch: imagePatch("image.tag", "v2")},
		{name: "descendant of bundle image path", patch: imagePatch("image.tag.digest", "sha256:abc")},
		{name: "no image path", patch: imagePatch("message", "hello")},
		{name: "image path outside bundle", patch: imagePatch("sidecar.image.tag", "v2"), wantErr: "non_bundle_image"},
		// The console compares paths byte-exactly after deciding the path is
		// about an image, so a different casing is outside the bundle.
		{name: "case mismatch", patch: imagePatch("Image.Tag", "v2"), wantErr: "non_bundle_image"},
		// ValuesPath "image.tag" is owned, its parent "image" is not: setting the
		// whole image subtree would replace values the bundle never bound.
		{name: "parent of bundle image path", patch: imagePatch("image", "v2"), wantErr: "non_bundle_image"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			svc, st, cleanup := setupService(t)
			defer cleanup()
			seedDefinition(t, st)
			seedBundleWithImages(t, st, "bundle-images", imageBundleImages())

			resp, err := svc.CreateOperation(adminCtx(),
				createOperationWithPatch("bundle-images", testCase.patch, "idem-image-ownership"))
			if testCase.wantErr == "" {
				require.NoError(t, err)
				assert.NotEmpty(t, resp.Msg.GetOperationId())
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

// TestNonBundleImagePath pins the path rule itself, including the edge cases the
// integration test cannot cheaply construct (nil patch, bundle without images,
// an image entry without a values path, deterministic reporting).
func TestNonBundleImagePath(t *testing.T) {
	images := imageBundleImages()
	testCases := []struct {
		name   string
		patch  *structpb.Struct
		images []store.BundleImage
		want   string
	}{
		{name: "nil patch", patch: nil, images: images},
		{name: "empty patch", patch: &structpb.Struct{}, images: images},
		{name: "no image path", patch: imagePatch("message", "hello"), images: images},
		{name: "bundle image path", patch: imagePatch("image.tag", "v2"), images: images},
		{name: "descendant", patch: imagePatch("image.tag.digest", "x"), images: images},
		{name: "sibling", patch: imagePatch("sidecar.image.tag", "x"), images: images, want: "sidecar.image.tag"},
		{name: "case mismatch", patch: imagePatch("IMAGE.TAG", "x"), images: images, want: "IMAGE.TAG"},
		{name: "bundle without images", patch: imagePatch("image.tag", "x"), images: nil, want: "image.tag"},
		{name: "image without values path", patch: imagePatch("image.tag", "x"),
			images: []store.BundleImage{{ValuesPath: ""}}, want: "image.tag"},
		{name: "deterministic first offending path",
			patch: &structpb.Struct{Fields: map[string]*structpb.Value{
				"z.image": structpb.NewStringValue("z"),
				"a.image": structpb.NewStringValue("a"),
			}}, images: images, want: "a.image"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, nonBundleImagePath(testCase.patch, testCase.images))
		})
	}
}
