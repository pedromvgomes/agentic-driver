package claudecode

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // OpenPGP v4 fingerprints are defined as SHA-1; the key's identity is pinned by it, not signed by it.
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	// Register SHA-256, SHA-384 and SHA-512 for crypto.Hash.New.
	_ "crypto/sha256"
	_ "crypto/sha512"
)

// This file is the slice of OpenPGP (RFC 9580 / RFC 4880) needed to check one
// thing: that a detached signature over a document was made by a pinned RSA key.
// It parses a single v4 RSA public key and a single v4 binary-document signature
// and verifies them with the standard library. Everything else a general OpenPGP
// implementation handles — other key algorithms, subkeys, user IDs, certification
// and revocation, text-mode signatures, compression, encryption — is refused or
// ignored, because the trust anchor is a fingerprint compared against a constant
// and no part of the key's own certification is consulted.

// OpenPGP packet tags and signature subpacket types used below.
const (
	packetSignature = 2
	packetPublicKey = 6

	subpacketCreated    = 2
	subpacketExpires    = 3
	subpacketIssuerFpr  = 33
	subpacketCriticalFl = 0x80

	algoRSA = 1

	sigTypeBinary = 0x00
)

// hashes maps the OpenPGP hash algorithm IDs the verifier accepts. SHA-1 and
// weaker are absent on purpose: a signature that names one is refused.
var hashes = map[byte]crypto.Hash{
	8:  crypto.SHA256,
	9:  crypto.SHA384,
	10: crypto.SHA512,
}

// publicKey is a parsed OpenPGP v4 RSA primary key.
type publicKey struct {
	rsa     *rsa.PublicKey
	created time.Time
	// fingerprint is the upper-case hex v4 fingerprint, the form
	// SigningKeyFingerprint is written in.
	fingerprint string
	raw         [20]byte
}

// parsePublicKey reads an armored public key block holding exactly one primary
// key. Any other primary key in the block is an error rather than a second
// trusted signer; subkeys, user IDs and self-signatures are ignored.
func parsePublicKey(armored []byte) (publicKey, error) {
	body, err := dearmor(armored, "PUBLIC KEY BLOCK")
	if err != nil {
		return publicKey{}, err
	}
	packets, err := readPackets(body)
	if err != nil {
		return publicKey{}, err
	}

	var primaries [][]byte
	for _, p := range packets {
		if p.tag == packetPublicKey {
			primaries = append(primaries, p.body)
		}
	}
	if len(primaries) != 1 {
		return publicKey{}, fmt.Errorf("the key block holds %d primary keys, expected exactly 1", len(primaries))
	}
	body = primaries[0]

	// version | created (4) | algorithm | n | e
	if len(body) < 6 || body[0] != 4 { // [lydite:exclude_from_mutation][a 6-byte body fails reading the modulus instead]
		return publicKey{}, errors.New("the primary key is not an OpenPGP v4 key")
	}
	if body[5] != algoRSA {
		return publicKey{}, fmt.Errorf("the primary key uses algorithm %d, only RSA (%d) is supported", body[5], algoRSA)
	}

	rest := body[6:]
	n, rest, err := readMPI(rest)
	if err != nil {
		return publicKey{}, fmt.Errorf("read the RSA modulus: %w", err)
	}
	e, rest, err := readMPI(rest)
	if err != nil {
		return publicKey{}, fmt.Errorf("read the RSA exponent: %w", err)
	}
	if len(rest) != 0 {
		return publicKey{}, errors.New("trailing bytes after the RSA public key")
	}
	if !e.IsInt64() || e.Int64() < 3 || e.Int64()%2 == 0 || e.Int64() > 1<<31-1 {
		return publicKey{}, errors.New("the RSA exponent is not a usable odd integer")
	}

	// The v4 fingerprint is the SHA-1 of 0x99, the 2-byte body length, and the body.
	// Two MPIs of at most 8 KiB each cannot reach this; the bound keeps the
	// 16-bit length in the fingerprint input from truncating.
	if len(body) > math.MaxUint16 { // [lydite:exclude_from_mutation][unreachable: MPIs cap the body below 17 KiB]
		return publicKey{}, errors.New("the primary key is too long to fingerprint")
	}
	h := sha1.New()                                              //nolint:gosec // see the import.
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))}) // #nosec G115 -- len(body) is at most MaxUint16, checked above.
	h.Write(body)

	key := publicKey{
		rsa:     &rsa.PublicKey{N: n, E: int(e.Int64())},
		created: time.Unix(int64(binary.BigEndian.Uint32(body[1:5])), 0).UTC(),
	}
	copy(key.raw[:], h.Sum(nil))
	key.fingerprint = strings.ToUpper(hex.EncodeToString(key.raw[:]))
	return key, nil
}

// verifyDetached checks that armoredSig is a signature over data by this key.
//
// The signature must be a single v4 RSA signature of a binary document, hashed
// with SHA-256 or stronger, created no earlier than the key and not past its own
// expiry at now. If it names its issuer by fingerprint, that fingerprint must be
// this key's. A subpacket marked critical that this verifier does not interpret
// refuses the signature, as the format requires.
func (k publicKey) verifyDetached(data, armoredSig []byte, now time.Time) error {
	body, err := dearmor(armoredSig, "SIGNATURE")
	if err != nil {
		return err
	}
	packets, err := readPackets(body)
	if err != nil {
		return err
	}
	if len(packets) != 1 || packets[0].tag != packetSignature {
		return fmt.Errorf("the signature file holds %d packets, expected exactly one signature", len(packets))
	}
	sig := packets[0].body

	// version | type | public-key algorithm | hash algorithm | hashed length (2)
	fixed, rest, ok := take(sig, 6)
	if !ok || fixed[0] != 4 {
		return errors.New("the signature is not an OpenPGP v4 signature")
	}
	if fixed[1] != sigTypeBinary {
		return fmt.Errorf("the signature type is %#x, expected a binary-document signature", fixed[1])
	}
	if fixed[2] != algoRSA {
		return fmt.Errorf("the signature uses algorithm %d, only RSA (%d) is supported", fixed[2], algoRSA)
	}
	hash, known := hashes[fixed[3]]
	if !known {
		return fmt.Errorf("the signature hash algorithm %d is not SHA-256 or stronger", fixed[3])
	}

	hashed, rest, ok := take(rest, int(binary.BigEndian.Uint16(fixed[4:6])))
	if !ok {
		return errors.New("the signature is truncated")
	}
	unhashedLen, rest, ok := take(rest, 2)
	if !ok {
		return errors.New("the signature is truncated")
	}
	_, rest, ok = take(rest, int(binary.BigEndian.Uint16(unhashedLen)))
	if !ok {
		return errors.New("the signature is truncated")
	}
	leftTwo, rest, ok := take(rest, 2)
	if !ok {
		return errors.New("the signature is truncated")
	}

	sigMPI, tail, err := readMPI(rest)
	if err != nil {
		return fmt.Errorf("read the signature value: %w", err)
	}
	if len(tail) != 0 {
		return errors.New("trailing bytes after the signature value")
	}

	meta, err := readSubpackets(hashed)
	if err != nil {
		return err
	}
	if !meta.hasCreated {
		return errors.New("the signature has no creation time")
	}
	if meta.created.Before(k.created) {
		return errors.New("the signature predates the key that made it")
	}
	if meta.expires != 0 && now.After(meta.created.Add(time.Duration(meta.expires)*time.Second)) {
		return errors.New("the signature has expired")
	}
	if meta.issuerFpr != nil && !bytes.Equal(meta.issuerFpr, k.raw[:]) {
		return fmt.Errorf("the signature names issuer %s, not this key",
			strings.ToUpper(hex.EncodeToString(meta.issuerFpr)))
	}

	h := hash.New()
	h.Write(data)
	h.Write(fixed)
	h.Write(hashed)
	trailer := binary.BigEndian.AppendUint32([]byte{4, 0xff}, 6+uint32(len(hashed)))
	h.Write(trailer)
	digest := h.Sum(nil)

	if !bytes.Equal(leftTwo, digest[:2]) {
		return errors.New("the signature does not match the document")
	}

	// An MPI drops leading zero bytes; PKCS#1 v1.5 verification wants the full
	// modulus width.
	size := (k.rsa.N.BitLen() + 7) / 8
	value := sigMPI.Bytes()
	if len(value) > size {
		return errors.New("the signature value is longer than the key")
	}
	padded := make([]byte, size)
	copy(padded[size-len(value):], value)

	if err := rsa.VerifyPKCS1v15(k.rsa, hash, digest, padded); err != nil {
		return errors.New("the signature does not match the document")
	}
	return nil
}

// packet is one OpenPGP packet's tag and body.
type packet struct {
	tag  byte
	body []byte
}

// readPackets splits binary OpenPGP data into packets. Indeterminate and
// partial lengths are refused; neither appears in keys or detached signatures.
func readPackets(data []byte) ([]packet, error) {
	var packets []packet
	for len(data) > 0 {
		header := data[0]
		if header&0x80 == 0 {
			return nil, errors.New("malformed OpenPGP packet header")
		}
		data = data[1:]

		var tag byte
		var length int
		if header&0x40 != 0 {
			tag = header & 0x3f
			if len(data) < 1 {
				return nil, errors.New("truncated OpenPGP packet length")
			}
			switch first := int(data[0]); {
			case first < 192:
				length, data = first, data[1:]
			case first < 224:
				if len(data) < 2 { // [lydite:exclude_from_mutation][a 2-byte input declares at least 192 body bytes and fails below]
					return nil, errors.New("truncated OpenPGP packet length")
				}
				length, data = (first-192)<<8+int(data[1])+192, data[2:]
			case first == 255:
				if len(data) < 5 {
					return nil, errors.New("truncated OpenPGP packet length")
				}
				length, data = int(binary.BigEndian.Uint32(data[1:5])), data[5:]
			default:
				return nil, errors.New("partial-length OpenPGP packets are not supported")
			}
		} else {
			tag = (header >> 2) & 0x0f
			width := 1 << (header & 0x03)
			if header&0x03 == 3 || len(data) < width {
				return nil, errors.New("indeterminate or truncated OpenPGP packet length")
			}
			for _, b := range data[:width] {
				length = length<<8 | int(b)
			}
			data = data[width:]
		}

		if length < 0 || length > len(data) {
			return nil, errors.New("OpenPGP packet is longer than the data")
		}
		packets = append(packets, packet{tag: tag, body: data[:length]})
		data = data[length:]
	}
	return packets, nil
}

// readMPI reads one multiprecision integer: a 2-byte bit count, then the value.
// take splits n bytes off the front of b.
func take(b []byte, n int) (head, rest []byte, ok bool) {
	if n < 0 || n > len(b) {
		return nil, nil, false
	}
	return b[:n], b[n:], true
}

func readMPI(data []byte) (*big.Int, []byte, error) {
	if len(data) < 2 { // [lydite:exclude_from_mutation][a 2-byte input has a zero bit count and fails below]
		return nil, nil, errors.New("truncated integer")
	}
	bits := int(binary.BigEndian.Uint16(data[:2]))
	size := (bits + 7) / 8
	if bits == 0 || len(data) < 2+size {
		return nil, nil, errors.New("truncated or empty integer")
	}
	value := new(big.Int).SetBytes(data[2 : 2+size])
	if value.BitLen() != bits {
		return nil, nil, errors.New("integer length does not match its bit count")
	}
	return value, data[2+size:], nil
}

// subpacketData is what the verifier reads out of a signature's hashed area.
type subpacketData struct {
	created    time.Time
	hasCreated bool
	expires    uint32
	issuerFpr  []byte
}

// readSubpackets walks the hashed subpacket area. Only the creation time,
// expiry and issuer fingerprint are interpreted; any other subpacket is skipped
// unless it is marked critical.
func readSubpackets(area []byte) (subpacketData, error) {
	var out subpacketData
	for len(area) > 0 {
		var length int
		switch first := int(area[0]); {
		case first < 192:
			length, area = first, area[1:]
		case first < 255:
			if len(area) < 2 {
				return out, errors.New("truncated signature subpacket length")
			}
			length, area = (first-192)<<8+int(area[1])+192, area[2:]
		default:
			if len(area) < 5 {
				return out, errors.New("truncated signature subpacket length")
			}
			length, area = int(binary.BigEndian.Uint32(area[1:5])), area[5:]
		}
		if length < 1 || length > len(area) {
			return out, errors.New("malformed signature subpacket")
		}

		kind := area[0] &^ subpacketCriticalFl
		critical := area[0]&subpacketCriticalFl != 0
		data := area[1:length]
		area = area[length:]

		switch kind {
		case subpacketCreated:
			if len(data) != 4 {
				return out, errors.New("malformed signature creation time")
			}
			out.created = time.Unix(int64(binary.BigEndian.Uint32(data)), 0).UTC()
			out.hasCreated = true
		case subpacketExpires:
			if len(data) != 4 {
				return out, errors.New("malformed signature expiry")
			}
			out.expires = binary.BigEndian.Uint32(data)
		case subpacketIssuerFpr:
			if len(data) != 21 || data[0] != 4 {
				return out, errors.New("malformed signature issuer fingerprint")
			}
			out.issuerFpr = data[1:]
		default:
			if critical {
				return out, fmt.Errorf("the signature carries a critical subpacket (type %d) this verifier does not understand", kind)
			}
		}
	}
	return out, nil
}

// dearmor decodes the first ASCII-armored block of the given type
// ("PUBLIC KEY BLOCK", "SIGNATURE"). The armor checksum line is not consulted:
// it guards against transmission damage, and what a damaged key or signature
// would break is caught by the fingerprint and the signature check.
func dearmor(data []byte, blockType string) ([]byte, error) {
	begin := "-----BEGIN PGP " + blockType + "-----"
	end := "-----END PGP " + blockType + "-----"

	lines := strings.Split(string(data), "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == begin {
			break
		}
	}
	if i == len(lines) {
		return nil, fmt.Errorf("no %q armor block", blockType)
	}
	i++ // [lydite:exclude_from_mutation][the header scan below also skips the BEGIN line]

	// Armor headers end at the first blank line.
	for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
	}

	var b64 strings.Builder
	terminated := false
	for i++; i < len(lines); i++ { // [lydite:exclude_from_mutation][starting on the blank line adds an empty string to the base64 text]
		line := strings.TrimSpace(lines[i])
		if line == end {
			terminated = true
			break
		}
		if strings.HasPrefix(line, "=") {
			// The checksum line; the end marker follows it.
			continue
		}
		b64.WriteString(line)
	}
	if !terminated {
		return nil, fmt.Errorf("the %q armor block is not terminated", blockType)
	}

	decoded, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, fmt.Errorf("decode the %q armor block: %w", blockType, err)
	}
	return decoded, nil
}
