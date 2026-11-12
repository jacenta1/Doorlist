package core

import "errors"

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var b58Index = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(b58Alphabet); i++ {
		t[b58Alphabet[i]] = int8(i)
	}
	return t
}()

// Base58Encode encodes bytes with the Bitcoin alphabet NEAR uses.
func Base58Encode(in []byte) string {
	zeros := 0
	for zeros < len(in) && in[zeros] == 0 {
		zeros++
	}
	buf := make([]byte, len(in)*138/100+1)
	size := 0
	for _, b := range in[zeros:] {
		carry := int(b)
		i := 0
		for j := len(buf) - 1; (carry != 0 || i < size) && j >= 0; j-- {
			carry += 256 * int(buf[j])
			buf[j] = byte(carry % 58)
			carry /= 58
			i++
		}
		size = i
	}
	start := len(buf) - size
	out := make([]byte, 0, zeros+size)
	for i := 0; i < zeros; i++ {
		out = append(out, '1')
	}
	for _, d := range buf[start:] {
		out = append(out, b58Alphabet[d])
	}
	return string(out)
}

// Base58Decode decodes a base58 string.
func Base58Decode(s string) ([]byte, error) {
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	buf := make([]byte, len(s)*733/1000+1)
	size := 0
	for i := zeros; i < len(s); i++ {
		v := b58Index[s[i]]
		if v < 0 {
			return nil, errors.New("invalid base58")
		}
		carry := int(v)
		k := 0
		for j := len(buf) - 1; (carry != 0 || k < size) && j >= 0; j-- {
			carry += 58 * int(buf[j])
			buf[j] = byte(carry % 256)
			carry /= 256
			k++
		}
		size = k
	}
	out := make([]byte, zeros, zeros+size)
	return append(out, buf[len(buf)-size:]...), nil
}

// ParseEd25519Key parses "ed25519:<base58>".
func ParseEd25519Key(s string) ([32]byte, error) {
	var k [32]byte
	const p = "ed25519:"
	if len(s) <= len(p) || s[:len(p)] != p {
		return k, ErrBadInput
	}
	b, err := Base58Decode(s[len(p):])
	if err != nil || len(b) != 32 {
		return k, ErrBadInput
	}
	copy(k[:], b)
	return k, nil
}

// FormatEd25519Key formats a raw public key as "ed25519:<base58>".
func FormatEd25519Key(pub []byte) string { return "ed25519:" + Base58Encode(pub) }
