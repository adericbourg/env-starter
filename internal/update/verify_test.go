package update

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

// signBlob mimics `cosign sign-blob --key`: ECDSA over the SHA-256 digest of
// the blob, ASN.1 DER then base64. It returns the public key PEM and signature.
func signBlob(t *testing.T, blob []byte) (pubPEM string, sigB64 []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	digest := sha256.Sum256(blob)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	pkix, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix}))
	sigB64 = []byte(base64.StdEncoding.EncodeToString(der))
	return pubPEM, sigB64
}

func TestVerifyBlobSignature_withValidSignature_returnsNil(t *testing.T) {
	// Given a blob signed by a known key.
	blob := []byte("abc123  env-starter_1.0.0_linux_amd64.tar.gz\n")
	pub, sig := signBlob(t, blob)

	// When / Then
	if err := verifyBlobSignature(pub, blob, sig); err != nil {
		t.Errorf("expected valid signature to verify, got: %v", err)
	}
}

func TestVerifyBlobSignature_withTamperedBlob_returnsError(t *testing.T) {
	// Given a signature over the original blob.
	blob := []byte("abc123  env-starter_1.0.0_linux_amd64.tar.gz\n")
	pub, sig := signBlob(t, blob)

	// When the blob is altered after signing.
	tampered := []byte("dead00  env-starter_1.0.0_linux_amd64.tar.gz\n")

	// Then verification fails.
	if err := verifyBlobSignature(pub, tampered, sig); err == nil {
		t.Error("expected verification to fail for a tampered blob, got nil")
	}
}

func TestVerifyBlobSignature_withWrongKey_returnsError(t *testing.T) {
	// Given a blob signed by one key but verified with another.
	blob := []byte("payload")
	_, sig := signBlob(t, blob)
	otherPub, _ := signBlob(t, []byte("unrelated"))

	// When / Then
	if err := verifyBlobSignature(otherPub, blob, sig); err == nil {
		t.Error("expected verification to fail with a mismatched key, got nil")
	}
}

func TestExtractBundleSignature_withValidBundle_returnsSignature(t *testing.T) {
	// Given a Sigstore bundle wrapping a known base64 signature.
	bundle := []byte(`{"messageSignature":{"signature":"deadbeef=="}}`)

	// When
	sig, err := extractBundleSignature(bundle)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(sig) != "deadbeef==" {
		t.Errorf("signature = %q, want %q", sig, "deadbeef==")
	}
}

func TestExtractBundleSignature_ofInvalidJSON_returnsError(t *testing.T) {
	if _, err := extractBundleSignature([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestExtractBundleSignature_whenSignatureMissing_returnsError(t *testing.T) {
	if _, err := extractBundleSignature([]byte(`{"messageSignature":{}}`)); err == nil {
		t.Error("expected error when messageSignature.signature is missing, got nil")
	}
}
