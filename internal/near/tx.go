// Package near is the small slice of NEAR the server needs: signing
// function-call transactions for the relayer, JSON-RPC calls, and checking a
// NEP-413 signed message for host sign-in.
package near

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"

	"doorlist/contract/core"
)

// Action is one NEAR transaction action.
type Action interface{ encode(b *borsh) }

// CreateAccount creates the receiver account (a sub-account of the signer).
type CreateAccount struct{}

// DeployContract deploys code to the receiver.
type DeployContract struct{ Code []byte }

// Transfer sends yoctoNEAR to the receiver.
type Transfer struct{ Deposit *big.Int }

// AddFullAccessKey adds a full-access key to the receiver.
type AddFullAccessKey struct{ PublicKey ed25519.PublicKey }

func (CreateAccount) encode(b *borsh)      { b.u8(0) }
func (a DeployContract) encode(b *borsh)   { b.u8(1); b.vec(a.Code) }
func (a Transfer) encode(b *borsh)         { b.u8(3); b.u128(a.Deposit) }
func (a AddFullAccessKey) encode(b *borsh) { b.u8(5); b.u8(0); b.Write(a.PublicKey); b.u64(0); b.u8(1) }
func (a FunctionCall) encode(b *borsh) {
	b.u8(2)
	b.str(a.Method)
	b.vec(a.Args)
	b.u64(a.Gas)
	b.u128(a.Deposit)
}

// FunctionCall calls a contract method.
type FunctionCall struct {
	Method  string
	Args    []byte
	Gas     uint64
	Deposit *big.Int // yoctoNEAR
}

// Transaction is a NEAR transaction with function-call actions.
type Transaction struct {
	SignerID   string
	PublicKey  ed25519.PublicKey
	Nonce      uint64
	ReceiverID string
	BlockHash  [32]byte
	Actions    []Action
}

type borsh struct{ bytes.Buffer }

func (b *borsh) u8(v uint8)   { b.WriteByte(v) }
func (b *borsh) u32(v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func (b *borsh) u64(v uint64) { _ = binary.Write(b, binary.LittleEndian, v) }
func (b *borsh) str(s string) { b.u32(uint32(len(s))); b.WriteString(s) }
func (b *borsh) vec(p []byte) { b.u32(uint32(len(p))); b.Write(p) }
func (b *borsh) u128(v *big.Int) {
	var out [16]byte
	if v != nil {
		be := v.Bytes()
		for i := 0; i < len(be) && i < 16; i++ {
			out[i] = be[len(be)-1-i]
		}
	}
	b.Write(out[:])
}

// Serialize returns the borsh encoding of the transaction.
func (t *Transaction) Serialize() []byte {
	var b borsh
	b.str(t.SignerID)
	b.u8(0) // ed25519
	b.Write(t.PublicKey)
	b.u64(t.Nonce)
	b.str(t.ReceiverID)
	b.Write(t.BlockHash[:])
	b.u32(uint32(len(t.Actions)))
	for _, a := range t.Actions {
		a.encode(&b)
	}
	return b.Bytes()
}

// Sign returns the borsh-encoded SignedTransaction and its hash.
func (t *Transaction) Sign(priv ed25519.PrivateKey) (signed []byte, hash [32]byte) {
	raw := t.Serialize()
	hash = sha256.Sum256(raw)
	sig := ed25519.Sign(priv, hash[:])
	var b borsh
	b.Write(raw)
	b.u8(0)
	b.Write(sig)
	return b.Bytes(), hash
}

// ParsePrivateKey reads "ed25519:<base58 64 bytes>" as near-cli writes it.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "ed25519:")
	b, err := core.Base58Decode(s)
	if err != nil {
		return nil, err
	}
	switch len(b) {
	case 64:
		return ed25519.PrivateKey(b), nil
	case 32:
		return ed25519.NewKeyFromSeed(b), nil
	}
	return nil, errors.New("near: private key must be 32 or 64 bytes")
}

// PublicKeyString formats a key as "ed25519:<base58>".
func PublicKeyString(pub ed25519.PublicKey) string { return core.FormatEd25519Key(pub) }

// ParseYocto parses a decimal yoctoNEAR string.
func ParseYocto(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return new(big.Int)
	}
	return v
}
