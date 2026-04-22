package api

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptorRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewEncryptor(key)
	require.NoError(t, err)

	plaintext := "super-secret-api-key"
	ciphertext, err := enc.Encrypt(plaintext)
	require.NoError(t, err)
	require.NotEmpty(t, ciphertext)

	decrypted, err := enc.Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptorDifferentCiphertexts(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewEncryptor(key)
	require.NoError(t, err)

	plaintext := "same-plaintext"
	ct1, err := enc.Encrypt(plaintext)
	require.NoError(t, err)
	ct2, err := enc.Encrypt(plaintext)
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2, "two encryptions of the same plaintext should differ (random nonce)")
}

func TestEncryptorBadKeyLength(t *testing.T) {
	_, err := NewEncryptor([]byte("too-short"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")

	_, err = NewEncryptor(make([]byte, 64))
	assert.Error(t, err)
}

func TestEncryptorDecryptTampered(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	enc, err := NewEncryptor(key)
	require.NoError(t, err)

	ct, err := enc.Encrypt("secret")
	require.NoError(t, err)

	// Tamper with ciphertext by flipping a character
	tampered := []byte(ct)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}

	_, err = enc.Decrypt(string(tampered))
	assert.Error(t, err)
}

func TestEncryptorDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	_, err := rand.Read(key1)
	require.NoError(t, err)
	_, err = rand.Read(key2)
	require.NoError(t, err)

	enc1, err := NewEncryptor(key1)
	require.NoError(t, err)
	enc2, err := NewEncryptor(key2)
	require.NoError(t, err)

	ct, err := enc1.Encrypt("secret-data")
	require.NoError(t, err)

	_, err = enc2.Decrypt(ct)
	assert.Error(t, err)
}
