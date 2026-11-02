package skillbundle

import (
	"testing"

	"github.com/nstranquist/wip-commit/internal/contract"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

func TestEmbeddedContractMatchesCommandContract(t *testing.T) {
	body, err := FS.ReadFile("contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var embedded struct {
		contract.Document
		PortableBundleSHA256 string `json:"portable_bundle_sha256"`
	}
	if err := strictjson.Decode(body, &embedded); err != nil {
		t.Fatal(err)
	}
	wanted, got := contract.Current(), embedded
	if wanted.SchemaVersion != got.SchemaVersion || wanted.Protocol != got.Protocol || wanted.PortableSkillContract != got.PortableSkillContract {
		t.Fatalf("embedded contract = %#v, want %#v", got, wanted)
	}
	if len(wanted.Capabilities) != len(got.Capabilities) {
		t.Fatalf("embedded capabilities = %v, want %v", got.Capabilities, wanted.Capabilities)
	}
	for index := range wanted.Capabilities {
		if wanted.Capabilities[index] != got.Capabilities[index] {
			t.Fatalf("embedded capabilities = %v, want %v", got.Capabilities, wanted.Capabilities)
		}
	}
	if digest, err := PayloadDigest(); err != nil || len(digest) != len("sha256:")+64 {
		t.Fatalf("payload digest = %q, %v", digest, err)
	}
	payloadValue, payloadErr := PayloadDigest()
	bundleValue, bundleErr := BundleDigest()
	if payload, bundle := mustDigest(t, payloadValue, payloadErr), mustDigest(t, bundleValue, bundleErr); payload == bundle {
		t.Fatalf("payload and complete bundle digests unexpectedly match: %s", payload)
	} else if embedded.PortableBundleSHA256 != payload {
		t.Fatalf("embedded portable digest = %s, want %s", embedded.PortableBundleSHA256, payload)
	}
}

func mustDigest(t *testing.T, digest string, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
