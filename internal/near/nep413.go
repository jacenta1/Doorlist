package near

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"doorlist/contract/core"
)

// nep413Tag is 2^31 + 413, the prefix that keeps a signed message from ever
// being a valid transaction.
const nep413Tag = uint32(2147484061)

// SignedMessage is what a wallet returns from signMessage (NEP-413).
type SignedMessage struct {
	AccountID string `json:"accountId"`
	PublicKey string `json:"publicKey"`
	Signature string `json:"signature"` // base64
}

// NEP413Hash is sha256(borsh(tag) || borsh(payload)).
func NEP413Hash(message string, nonce [32]byte, recipient string, callbackURL *string) [32]byte {
	var b borsh
	b.u32(nep413Tag)
	b.str(message)
	b.Write(nonce[:])
	b.str(recipient)
	if callbackURL == nil {
		b.u8(0)
	} else {
		b.u8(1)
		b.str(*callbackURL)
	}
	return sha256.Sum256(b.Bytes())
}

// VerifyNEP413 checks the signature only.
func VerifyNEP413(sm SignedMessage, message string, nonce [32]byte, recipient string) error {
	pub, err := core.ParseEd25519Key(sm.PublicKey)
	if err != nil {
		return errors.New("nep413: bad public key")
	}
	sig, err := base64.StdEncoding.DecodeString(sm.Signature)
	if err != nil || len(sig) != 64 {
		return errors.New("nep413: bad signature encoding")
	}
	h := NEP413Hash(message, nonce, recipient, nil)
	if !ed25519.Verify(pub[:], h[:], sig) {
		return errors.New("nep413: signature does not verify")
	}
	return nil
}

// VerifySignIn checks the signature and that the key is a full-access key of
// the account, so a signature from any random key cannot claim an account.
func VerifySignIn(ctx context.Context, rpc *RPC, sm SignedMessage, message string, nonce [32]byte, recipient string) error {
	if err := VerifyNEP413(sm, message, nonce, recipient); err != nil {
		return err
	}
	if !core.ValidAccount(sm.AccountID) {
		return errors.New("nep413: bad account id")
	}
	_, full, err := rpc.AccessKey(ctx, sm.AccountID, sm.PublicKey)
	if err != nil {
		return err
	}
	if !full {
		return errors.New("nep413: key is not a full-access key of the account")
	}
	return nil
}
