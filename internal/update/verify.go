package update

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// cosignPublicKeyPEM is the project's cosign public key (PKIX PEM). Self-update
// verifies that checksums.txt was signed by the matching private key before
// trusting any digest in it, so a tampered or swapped release cannot be
// installed even if TLS to GitHub is compromised.
//
// If this constant is ever cleared, self-update is disabled entirely: Apply
// returns an error and no binary is replaced (see docs/releasing.md).
const cosignPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEm+sOgpB6s2xTuf+9XueSpRlXwDIH
gygDTfQ7op0yrp2Dh7Ub4MX/YIEQRxj1RNk6l0InsDICLekXmyMPqkBsGw==
-----END PUBLIC KEY-----
`

// verifyBlobSignature reports whether sigB64 is a valid cosign signature of blob
// produced by the private key matching pubKeyPEM. It mirrors `cosign
// verify-blob --key`: ECDSA over the SHA-256 digest of the blob, with the
// signature ASN.1 DER-encoded then base64-encoded.
func verifyBlobSignature(pubKeyPEM string, blob, sigB64 []byte) error {
	block, _ := pem.Decode([]byte(pubKeyPEM))
	if block == nil {
		return errors.New("update: invalid public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("update: parsing public key: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("update: unexpected public key type %T (want ECDSA)", pub)
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("update: decoding signature: %w", err)
	}

	digest := sha256.Sum256(blob)
	if !ecdsa.VerifyASN1(ecPub, digest[:], sig) {
		return errors.New("update: checksums signature verification failed")
	}
	return nil
}

// sigstoreBundle is the minimal slice of the Sigstore bundle schema
// (github.com/sigstore/protobuf-specs, bundle.proto) needed to recover a
// key-based messageSignature, as written by `cosign sign-blob --bundle`
// (cosign v3+, replacing the old --output-signature raw signature file).
type sigstoreBundle struct {
	MessageSignature struct {
		Signature string `json:"signature"`
	} `json:"messageSignature"`
}

// extractBundleSignature pulls the base64 ECDSA signature out of a cosign v3
// Sigstore bundle, for use with verifyBlobSignature.
func extractBundleSignature(bundleJSON []byte) ([]byte, error) {
	var bundle sigstoreBundle
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		return nil, fmt.Errorf("update: parsing sigstore bundle: %w", err)
	}
	if bundle.MessageSignature.Signature == "" {
		return nil, errors.New("update: sigstore bundle missing messageSignature.signature")
	}
	return []byte(bundle.MessageSignature.Signature), nil
}
