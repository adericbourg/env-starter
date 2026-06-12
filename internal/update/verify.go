package update

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// cosignPublicKeyPEM is the project's cosign public key (PKIX PEM). When set,
// self-update verifies that checksums.txt was signed by the matching private
// key before trusting any digest in it, so a tampered or swapped release cannot
// be installed even if TLS to GitHub is compromised.
//
// It is intentionally empty in source: a maintainer pastes the contents of
// cosign.pub here once the release keypair exists (see docs/releasing.md).
// While empty, signature verification is skipped and a warning is emitted.
const cosignPublicKeyPEM = ""

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
