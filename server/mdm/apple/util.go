package apple_mdm

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"time"
)

// Note Apple rejects CSRs if the key size is not 2048.
const rsaKeySize = 2048

// newPrivateKey creates an RSA private key
func newPrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, rsaKeySize)
}

// EncodeCertPEM returns PEM-endcoded certificate data.
func EncodeCertPEM(cert *x509.Certificate) []byte {
	block := pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}
	return pem.EncodeToMemory(&block)
}

func DecodeCertPEM(encoded []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, errors.New("no PEM-encoded data found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("unexpected block type %s", block.Type)
	}

	return x509.ParseCertificate(block.Bytes)
}

func EncodeCertRequestPEM(cert *x509.CertificateRequest) []byte {
	pemBlock := &pem.Block{
		Type:    "CERTIFICATE REQUEST",
		Headers: nil,
		Bytes:   cert.Raw,
	}

	return pem.EncodeToMemory(pemBlock)
}

// EncodePrivateKeyPEM returns PEM-encoded private key data
func EncodePrivateKeyPEM(key *rsa.PrivateKey) []byte {
	block := pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return pem.EncodeToMemory(&block)
}

// DecodePrivateKeyPEM decodes PEM-encoded private key data.
func DecodePrivateKeyPEM(encoded []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, errors.New("no PEM-encoded data found")
	}
	if block.Type != "RSA PRIVATE KEY" {
		return nil, fmt.Errorf("unexpected block type %s", block.Type)
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// GenerateRandomPin generates a `lenght`-digit PIN number that takes into
// account the current time as described in rfc4226 (for one time passwords)
//
// The implementation details have been mostly taken from https://github.com/pquerna/otp
func GenerateRandomPin(length int) string {
	counter := uint64(time.Now().Unix())
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	m := sha256.New()
	m.Write(buf)
	sum := m.Sum(nil)
	offset := sum[len(sum)-1] & 0xf
	value := int64(((int(sum[offset]) & 0x7f) << 24) |
		((int(sum[offset+1] & 0xff)) << 16) |
		((int(sum[offset+2] & 0xff)) << 8) |
		(int(sum[offset+3]) & 0xff))
	v := int32(value % int64(math.Pow10(length)))
	f := fmt.Sprintf("%%0%dd", length)
	return fmt.Sprintf(f, v)
}
