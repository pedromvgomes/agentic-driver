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
	"reflect"
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
	// oversizedValue makes the signature value one byte longer than the key.
	oversizedValue bool
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

	if spec.oversizedValue {
		value = append([]byte{0x01}, value...)
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
		"a value longer than the key":   {oversizedValue: true},
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

func TestTake(t *testing.T) {
	b := []byte{1, 2, 3}
	if head, rest, ok := take(b, 3); !ok || len(head) != 3 || len(rest) != 0 {
		t.Errorf("take(all) = %v, %v, %v", head, rest, ok)
	}
	if head, rest, ok := take(b, 0); !ok || len(head) != 0 || len(rest) != 3 {
		t.Errorf("take(0) = %v, %v, %v", head, rest, ok)
	}
	if _, _, ok := take(b, 4); ok {
		t.Error("take past the end succeeded")
	}
	if _, _, ok := take(b, -1); ok {
		t.Error("take of a negative count succeeded")
	}
}

func TestReadPackets(t *testing.T) {
	long := bytes.Repeat([]byte{7}, 300)
	five := bytes.Repeat([]byte{8}, 9000)

	accepted := map[string]struct {
		in   []byte
		want []packet
	}{
		"a new-format packet with no body":  {[]byte{0xc2, 0x00}, []packet{{tag: 2, body: []byte{}}}},
		"a new-format one-byte length":      {[]byte{0xc6, 0x02, 9, 9}, []packet{{tag: 6, body: []byte{9, 9}}}},
		"the longest one-byte length":       {append([]byte{0xc2, 191}, bytes.Repeat([]byte{1}, 191)...), nil},
		"the shortest two-byte length":      {append([]byte{0xc2, 192, 0}, bytes.Repeat([]byte{1}, 192)...), nil},
		"a two-byte length":                 {append([]byte{0xc2, 192, 108}, long...), nil},
		"a five-byte length":                {append([]byte{0xc2, 255, 0, 0, 0x23, 0x28}, five...), nil},
		"an old-format packet with no body": {[]byte{0x88, 0x00}, []packet{{tag: 2, body: []byte{}}}},
		"an old-format two-byte length":     {[]byte{0x89, 0x00, 0x01, 5}, []packet{{tag: 2, body: []byte{5}}}},
		"an old-format four-byte length":    {[]byte{0x8a, 0x00, 0x00, 0x00, 0x01, 5}, []packet{{tag: 2, body: []byte{5}}}},
		"two packets back to back":          {[]byte{0xc2, 0x01, 1, 0xc6, 0x01, 2}, []packet{{tag: 2, body: []byte{1}}, {tag: 6, body: []byte{2}}}},
		"no packets":                        {nil, nil},
	}
	for name, tc := range accepted {
		t.Run(name, func(t *testing.T) {
			got, err := readPackets(tc.in)
			if err != nil {
				t.Fatalf("readPackets: %v", err)
			}
			if tc.want != nil && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}

	refused := map[string][]byte{
		"a header without the high bit":              {0x42, 0x00},
		"a new-format header with no length":         {0xc2},
		"a partial length":                           {0xc2, 224, 1},
		"a two-byte length cut short":                {0xc2, 192},
		"a five-byte length cut short":               {0xc2, 255, 0, 0, 0},
		"a body shorter than its length":             {0xc2, 0x05, 1, 2},
		"an old-format indeterminate length":         {0x8b, 1},
		"an old-format length cut short":             {0x89, 0x00},
		"an old-format four-byte length cut":         {0x8a, 0, 0, 0},
		"a length byte reserved for partial lengths": append([]byte{0xc2, 224, 0}, make([]byte, 8384)...),
	}
	for name, in := range refused {
		t.Run(name, func(t *testing.T) {
			if _, err := readPackets(in); err == nil {
				t.Fatal("malformed packets were accepted")
			}
		})
	}
}

func TestReadMPI(t *testing.T) {
	if v, rest, err := readMPI([]byte{0x00, 0x09, 0x01, 0xff, 0xaa}); err != nil || v.Int64() != 0x1ff || len(rest) != 1 {
		t.Errorf("= %v, %v, %v", v, rest, err)
	}
	for name, in := range map[string][]byte{
		"empty":                 nil,
		"one byte":              {0x00},
		"a zero bit count":      {0x00, 0x00},
		"a value cut short":     {0x00, 0x10, 0x01},
		"a bit count that lies": {0x00, 0x10, 0x01, 0xff},
	} {
		if _, _, err := readMPI(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadSubpackets(t *testing.T) {
	filler := func(kind byte, n int) []byte {
		return append([]byte{kind}, bytes.Repeat([]byte{0}, n-1)...)
	}
	twoByte := append([]byte{192, 0}, filler(100, 192)...)
	fiveByte := append([]byte{255, 0, 0, 0x23, 0x28}, filler(100, 9000)...)
	empty := []byte{1, 100}
	created := subpacket(subpacketCreated, 0, 0, 0, 9)

	for name, area := range map[string][]byte{
		"an unknown subpacket with two-byte length":  append(bytes.Clone(twoByte), created...),
		"an unknown subpacket with five-byte length": append(bytes.Clone(fiveByte), created...),
		"an unknown subpacket with no data":          append(bytes.Clone(empty), created...),
		"the longest one-byte length":                append(append([]byte{191}, filler(100, 191)...), created...),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readSubpackets(area)
			if err != nil {
				t.Fatalf("readSubpackets: %v", err)
			}
			if !got.hasCreated || got.created.Unix() != 9 {
				t.Errorf("the creation time after the unknown subpacket was lost: %+v", got)
			}
		})
	}

	for name, area := range map[string][]byte{
		"a zero length":                {0},
		"a length past the area":       {5, 100, 1},
		"a two-byte length cut short":  {192},
		"a five-byte length cut short": {255, 0, 0, 0},
		"a short creation time":        subpacket(subpacketCreated, 1, 2),
		"a short expiry":               subpacket(subpacketExpires, 1, 2),
		"a malformed issuer":           subpacket(subpacketIssuerFpr, 4, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readSubpackets(area); err == nil {
				t.Fatal("a malformed subpacket area was accepted")
			}
		})
	}
}

func TestDearmor(t *testing.T) {
	body := armor("SIGNATURE", []byte("hello"))

	if got, err := dearmor(append([]byte("noise before\n"), body...), "SIGNATURE"); err != nil || string(got) != "hello" {
		t.Errorf("text before the block: %q, %v", got, err)
	}
	if got, err := dearmor(bytes.ReplaceAll(body, []byte("\n"), []byte("\r\n")), "SIGNATURE"); err != nil || string(got) != "hello" {
		t.Errorf("CRLF line endings: %q, %v", got, err)
	}
	withHeaders := bytes.Replace(body, []byte("-----\n\n"), []byte("-----\nVersion: x\nComment: y\n\n"), 1)
	if got, err := dearmor(withHeaders, "SIGNATURE"); err != nil || string(got) != "hello" {
		t.Errorf("armor headers: %q, %v", got, err)
	}
	withCRC := bytes.Replace(body, []byte("\n-----END"), []byte("\n=AAAA\n-----END"), 1)
	if got, err := dearmor(withCRC, "SIGNATURE"); err != nil || string(got) != "hello" {
		t.Errorf("checksum line: %q, %v", got, err)
	}

	for name, in := range map[string][]byte{
		"no blank line after the header": []byte("-----BEGIN PGP SIGNATURE-----\nVersion: x"),
		"a different block type":         armor("PUBLIC KEY BLOCK", []byte("x")),
		"BEGIN as the last line":         []byte("-----BEGIN PGP SIGNATURE-----"),
		"an END marker with no BEGIN":    []byte("junk\n\naGVsbG8=\n-----END PGP SIGNATURE-----\n"),
	} {
		if _, err := dearmor(in, "SIGNATURE"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParsePublicKeyExponentBounds(t *testing.T) {
	k := trustedTestKey()
	n := mpi(k.priv.N.Bytes())
	body := func(e int64) []byte {
		out := append(binary.BigEndian.AppendUint32([]byte{4}, uint32(k.created.Unix())), algoRSA)
		out = append(out, n...)
		return append(out, mpi(big.NewInt(e).Bytes())...)
	}
	parse := func(e int64) error {
		_, err := parsePublicKey(armor("PUBLIC KEY BLOCK", packetBytes(packetPublicKey, body(e))))
		return err
	}

	for _, e := range []int64{3, 65537, 1<<31 - 1} {
		if err := parse(e); err != nil {
			t.Errorf("exponent %d refused: %v", e, err)
		}
	}
	for _, e := range []int64{1, 2, 4, 1<<31 + 1} {
		if err := parse(e); err == nil {
			t.Errorf("exponent %d accepted", e)
		}
	}
}

// Every proper prefix of a valid signature or key is refused, and none of them
// panics.
func TestTruncatedInputsAreRefused(t *testing.T) {
	signer := trustedTestKey()
	key := signer.public(t)
	data := []byte("document")

	sigPacket := signer.sign(t, data, sigSpec{expiresAfter: 3600})
	raw, err := dearmor(sigPacket, "SIGNATURE")
	if err != nil {
		t.Fatalf("dearmor: %v", err)
	}
	packets, err := readPackets(raw)
	if err != nil || len(packets) != 1 {
		t.Fatalf("readPackets: %v", err)
	}
	body := packets[0].body
	for n := range len(body) {
		truncated := armor("SIGNATURE", packetBytes(packetSignature, body[:n]))
		if err := key.verifyDetached(data, truncated, time.Now()); err == nil {
			t.Fatalf("a signature cut to %d of %d bytes was accepted", n, len(body))
		}
	}

	keyBody := signer.keyBody()
	for n := range len(keyBody) {
		block := armor("PUBLIC KEY BLOCK", packetBytes(packetPublicKey, keyBody[:n]))
		if _, err := parsePublicKey(block); err == nil {
			t.Fatalf("a key cut to %d of %d bytes was accepted", n, len(keyBody))
		}
	}
}
