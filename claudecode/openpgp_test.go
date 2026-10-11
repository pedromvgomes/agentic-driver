package claudecode

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// testKey is a throwaway RSA signing key that can also write itself and its
// signatures out in OpenPGP form, so verification is exercised through the same
// parser the embedded key goes through.
type testKey struct {
	priv    *rsa.PrivateKey
	created time.Time
}

func generateTestKey() testKey {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return testKey{priv: priv, created: time.Now().Add(-24 * time.Hour).Truncate(time.Second).UTC()}
}

var (
	trustedTestKey  = sync.OnceValue(generateTestKey)
	impostorTestKey = sync.OnceValue(generateTestKey)
)

func mpi(b []byte) []byte {
	value := new(big.Int).SetBytes(b)
	bits := value.BitLen()
	return append([]byte{byte(bits >> 8), byte(bits)}, value.Bytes()...)
}

func packetBytes(tag byte, body []byte) []byte {
	out := []byte{0xc0 | tag}
	switch n := len(body); {
	case n < 192:
		out = append(out, byte(n))
	case n < 8384:
		out = append(out, byte((n-192)>>8+192), byte(n-192))
	default:
		out = binary.BigEndian.AppendUint32(append(out, 255), uint32(n))
	}
	return append(out, body...)
}

func armor(blockType string, data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	b.WriteString("-----BEGIN PGP " + blockType + "-----\n\n")
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n-----END PGP " + blockType + "-----\n")
	return []byte(b.String())
}

func (k testKey) keyBody() []byte {
	body := binary.BigEndian.AppendUint32([]byte{4}, uint32(k.created.Unix()))
	body = append(body, algoRSA)
	body = append(body, mpi(k.priv.N.Bytes())...)
	return append(body, mpi(big.NewInt(int64(k.priv.E)).Bytes())...)
}

// armoredPublic is the key as an armored public key block.
func (k testKey) armoredPublic() []byte {
	return armor("PUBLIC KEY BLOCK", packetBytes(packetPublicKey, k.keyBody()))
}

func (k testKey) public(t testing.TB) publicKey {
	t.Helper()
	key, err := parsePublicKey(k.armoredPublic())
	if err != nil {
		t.Fatalf("parse a generated key: %v", err)
	}
	return key
}

// sigSpec shapes a signature. The zero value is a well-formed SHA-512 binary
// signature made now, naming the key's own fingerprint.
type sigSpec struct {
	sigType      byte
	hashID       byte
	created      time.Time
	omitCreated  bool
	expiresAfter uint32
	issuer       []byte
	criticalType byte
	packets      int
}

func subpacket(kind byte, data ...byte) []byte {
	return append([]byte{byte(len(data) + 1), kind}, data...)
}

// sign writes an armored detached signature over data.
func (k testKey) sign(t testing.TB, data []byte, spec sigSpec) []byte {
	t.Helper()

	if spec.hashID == 0 {
		spec.hashID = 10
	}
	hash := hashes[spec.hashID]
	if hash == 0 {
		hash = crypto.SHA1
	}
	if spec.created.IsZero() {
		spec.created = time.Now()
	}

	pub := k.public(t)
	issuer := spec.issuer
	if issuer == nil {
		issuer = pub.raw[:]
	}

	hashed := subpacket(subpacketIssuerFpr, append([]byte{4}, issuer...)...)
	if !spec.omitCreated {
		hashed = append(hashed, subpacket(subpacketCreated,
			binary.BigEndian.AppendUint32(nil, uint32(spec.created.Unix()))...)...)
	}
	if spec.expiresAfter != 0 {
		hashed = append(hashed, subpacket(subpacketExpires,
			binary.BigEndian.AppendUint32(nil, spec.expiresAfter)...)...)
	}
	if spec.criticalType != 0 {
		hashed = append(hashed, subpacket(spec.criticalType|subpacketCriticalFl, 1)...)
	}

	header := append([]byte{4, spec.sigType, algoRSA, spec.hashID},
		byte(len(hashed)>>8), byte(len(hashed)))
	header = append(header, hashed...)

	h := hash.New()
	h.Write(data)
	h.Write(header)
	h.Write(binary.BigEndian.AppendUint32([]byte{4, 0xff}, uint32(len(header))))
	digest := h.Sum(nil)

	value, err := rsa.SignPKCS1v15(rand.Reader, k.priv, hash, digest)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	body := append(header, 0, 0) // no unhashed subpackets
	body = append(body, digest[:2]...)
	body = append(body, mpi(value)...)

	one := packetBytes(packetSignature, body)
	var all []byte
	for range max(spec.packets, 1) {
		all = append(all, one...)
	}
	return armor("SIGNATURE", all)
}

func (k testKey) signManifest(t testing.TB, data []byte) []byte {
	return k.sign(t, data, sigSpec{})
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func embeddedKey(t testing.TB) publicKey {
	t.Helper()
	key, err := parsePublicKey(signingKey)
	if err != nil {
		t.Fatalf("parse the embedded key: %v", err)
	}
	return key
}

// A real manifest and the real signature Anthropic published for it must verify
// against the embedded key. Everything synthetic below is only as good as this.
func TestTheEmbeddedKeyVerifiesARealSignedManifest(t *testing.T) {
	manifest := readFixture(t, "manifest-2.1.280.json")
	sig := readFixture(t, "manifest-2.1.280.json.sig")

	if err := embeddedKey(t).verifyDetached(manifest, sig, time.Now()); err != nil {
		t.Fatalf("a real signed manifest was refused: %v", err)
	}
}

func TestARealSignedManifestIsRefusedWhenAltered(t *testing.T) {
	manifest := readFixture(t, "manifest-2.1.280.json")
	sig := readFixture(t, "manifest-2.1.280.json.sig")
	key := embeddedKey(t)

	tests := map[string]func() ([]byte, []byte){
		"one byte of the manifest changed": func() ([]byte, []byte) {
			altered := bytes.Clone(manifest)
			altered[len(altered)/2] ^= 1
			return altered, sig
		},
		"a byte appended": func() ([]byte, []byte) {
			return append(bytes.Clone(manifest), ' '), sig
		},
		"the signature value changed": func() ([]byte, []byte) {
			// Flip one base64 character inside the signature value, away from the
			// packet header.
			altered := bytes.Clone(sig)
			i := len(altered) - 40
			if altered[i] == 'A' {
				altered[i] = 'B'
			} else {
				altered[i] = 'A'
			}
			return manifest, altered
		},
		"no signature": func() ([]byte, []byte) { return manifest, nil },
	}

	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			data, signature := alter()
			if err := key.verifyDetached(data, signature, time.Now()); err == nil {
				t.Fatal("an altered manifest or signature was accepted")
			}
		})
	}
}

func TestARealSignatureIsRefusedUnderAnotherKey(t *testing.T) {
	manifest := readFixture(t, "manifest-2.1.280.json")
	sig := readFixture(t, "manifest-2.1.280.json.sig")

	if err := impostorTestKey().public(t).verifyDetached(manifest, sig, time.Now()); err == nil {
		t.Fatal("a signature was accepted under a key that did not make it")
	}
}

func TestAGeneratedSignatureVerifiesOnlyForItsKeyAndDocument(t *testing.T) {
	data := []byte(`{"version":"9.9.9"}`)
	signer := trustedTestKey()
	sig := signer.signManifest(t, data)

	if err := signer.public(t).verifyDetached(data, sig, time.Now()); err != nil {
		t.Fatalf("a valid signature was refused: %v", err)
	}
	if err := signer.public(t).verifyDetached(append(bytes.Clone(data), ' '), sig, time.Now()); err == nil {
		t.Error("a tampered document was accepted")
	}
	if err := impostorTestKey().public(t).verifyDetached(data, sig, time.Now()); err == nil {
		t.Error("the wrong key was accepted")
	}
}

func TestMalformedSignaturesAreRefused(t *testing.T) {
	data := []byte("document")
	signer := trustedTestKey()
	key := signer.public(t)

	tests := map[string]sigSpec{
		"a text-mode signature":         {sigType: 0x01},
		"SHA-1":                         {hashID: 2},
		"an issuer that is not the key": {issuer: bytes.Repeat([]byte{0xab}, 20)},
		"no creation time":              {omitCreated: true},
		"made before the key existed":   {created: signer.created.Add(-time.Hour)},
		"expired":                       {created: time.Now().Add(-2 * time.Hour), expiresAfter: 3600},
		"an unknown critical subpacket": {criticalType: 99},
		"two signatures in one file":    {packets: 2},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			if err := key.verifyDetached(data, signer.sign(t, data, spec), time.Now()); err == nil {
				t.Fatal("a signature that should be refused was accepted")
			}
		})
	}

	t.Run("an expiry still in the future is honoured", func(t *testing.T) {
		sig := signer.sign(t, data, sigSpec{expiresAfter: 3600})
		if err := key.verifyDetached(data, sig, time.Now()); err != nil {
			t.Fatalf("a signature within its lifetime was refused: %v", err)
		}
	})
}

func TestUnparseableSignatureInputIsRefused(t *testing.T) {
	key := trustedTestKey().public(t)
	good := trustedTestKey().signManifest(t, []byte("x"))

	for name, sig := range map[string][]byte{
		"empty":            nil,
		"a public key":     trustedTestKey().armoredPublic(),
		"not armor":        []byte("hello"),
		"unterminated":     good[:len(good)-30],
		"bad base64":       []byte("-----BEGIN PGP SIGNATURE-----\n\n!!!!\n-----END PGP SIGNATURE-----\n"),
		"truncated packet": armor("SIGNATURE", []byte{0xc2, 0x40, 0x04}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := key.verifyDetached([]byte("x"), sig, time.Now()); err == nil {
				t.Fatal("unparseable input was accepted")
			}
		})
	}
}

func TestParsePublicKeyRequiresExactlyOnePrimaryKey(t *testing.T) {
	a, b := trustedTestKey(), impostorTestKey()
	both := armor("PUBLIC KEY BLOCK", append(
		packetBytes(packetPublicKey, a.keyBody()), packetBytes(packetPublicKey, b.keyBody())...))

	if _, err := parsePublicKey(both); err == nil {
		t.Error("a block with two primary keys was accepted")
	}
	if _, err := parsePublicKey(armor("PUBLIC KEY BLOCK", nil)); err == nil {
		t.Error("a block with no key was accepted")
	}
	if _, err := parsePublicKey([]byte("garbage")); err == nil {
		t.Error("garbage was accepted")
	}
}

func TestParsePublicKeyRefusesADefectiveKeyBody(t *testing.T) {
	k := trustedTestKey()
	n := mpi(k.priv.N.Bytes())
	header := func(version, algo byte) []byte {
		return append(binary.BigEndian.AppendUint32([]byte{version}, uint32(k.created.Unix())), algo)
	}
	body := func(version, algo byte, e int64, trailer ...byte) []byte {
		out := append(header(version, algo), n...)
		out = append(out, mpi(big.NewInt(e).Bytes())...)
		return append(out, trailer...)
	}

	tests := map[string][]byte{
		"a v3 key":                    body(3, algoRSA, 65537),
		"a non-RSA algorithm":         body(4, 22, 65537),
		"an even exponent":            body(4, algoRSA, 65536),
		"an exponent below 3":         body(4, algoRSA, 1),
		"an exponent that is too big": body(4, algoRSA, 1<<32+1),
		"trailing bytes":              body(4, algoRSA, 65537, 0x00),
		"a body too short to read":    header(4, algoRSA)[:4],
		"a truncated modulus":         append(header(4, algoRSA), n[:10]...),
	}

	for name, keyBody := range tests {
		t.Run(name, func(t *testing.T) {
			block := armor("PUBLIC KEY BLOCK", packetBytes(packetPublicKey, keyBody))
			if _, err := parsePublicKey(block); err == nil {
				t.Fatal("a defective key was accepted")
			}
		})
	}

	t.Run("the well-formed body is accepted", func(t *testing.T) {
		block := armor("PUBLIC KEY BLOCK", packetBytes(packetPublicKey, body(4, algoRSA, 65537)))
		if _, err := parsePublicKey(block); err != nil {
			t.Fatalf("a well-formed key was refused: %v", err)
		}
	})
}
