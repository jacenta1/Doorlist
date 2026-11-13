package near

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"testing"

	"doorlist/contract/core"
)

func TestTransactionLayout(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, 32))
	tx := &Transaction{SignerID: "a.near", PublicKey: priv.Public().(ed25519.PublicKey), Nonce: 7, ReceiverID: "b.near",
		Actions: []Action{FunctionCall{Method: "m", Args: []byte("{}"), Gas: 5, Deposit: big.NewInt(1)}}}
	b := tx.Serialize()
	// signer: u32 len + "a.near"
	if binary.LittleEndian.Uint32(b) != 6 || string(b[4:10]) != "a.near" || b[10] != 0 {
		t.Fatalf("signer/key prefix: %x", b[:12])
	}
	off := 10 + 1 + 32
	if binary.LittleEndian.Uint64(b[off:]) != 7 {
		t.Fatal("nonce")
	}
	// action tag 2 = FunctionCall, deposit u128 little-endian = 1 at the end
	if b[len(b)-16] != 1 || b[len(b)-16-8-2-4-1-4-1] != 2 {
		t.Fatalf("action layout: %x", b[len(b)-40:])
	}
	signed, hash := tx.Sign(priv)
	if hash != sha256.Sum256(b) || !ed25519.Verify(priv.Public().(ed25519.PublicKey), hash[:], signed[len(b)+1:]) {
		t.Fatal("signature")
	}
}

func TestNEP413(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	var nonce [32]byte
	nonce[0] = 9
	h := NEP413Hash("Sign in to Doorlist as a host", nonce, "doorlist", nil)
	sm := SignedMessage{AccountID: "host.near", PublicKey: core.FormatEd25519Key(pub), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, h[:]))}
	if err := VerifyNEP413(sm, "Sign in to Doorlist as a host", nonce, "doorlist"); err != nil {
		t.Fatal(err)
	}
	if VerifyNEP413(sm, "Sign in to something else", nonce, "doorlist") == nil {
		t.Fatal("wrong message verified")
	}
	nonce[0] = 8
	if VerifyNEP413(sm, "Sign in to Doorlist as a host", nonce, "doorlist") == nil {
		t.Fatal("wrong nonce verified")
	}
}

func TestParsePrivateKey(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, 32))
	k, err := ParsePrivateKey("ed25519:" + core.Base58Encode(priv))
	if err != nil || !k.Equal(priv) {
		t.Fatal(err)
	}
}
