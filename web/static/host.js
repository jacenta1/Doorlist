// host.js: live-mode host sign-in. The wallet signs a NEP-413 message (no
// transaction); the server checks the signature and that the key is a
// full-access key of the account.
import { NearConnector } from "https://cdn.jsdelivr.net/npm/@hot-labs/near-connect@0.11.4/+esm";

const btn = document.getElementById("signin");
const status = document.getElementById("signin-status");
const nonceB64 = btn.dataset.nonce;
const nonce = Uint8Array.from(atob(nonceB64), (c) => c.charCodeAt(0));
const connector = new NearConnector({ network: btn.dataset.network || "testnet" });

connector.on("wallet:signInAndSignMessage", async (t) => {
  const a = t.accounts[0];
  const sm = a.signedMessage;
  status.textContent = "Checking the signature…";
  const res = await fetch("/host/signin", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ nonce: nonceB64, signed: { accountId: a.accountId, publicKey: sm.publicKey, signature: sm.signature } }),
  });
  if (res.ok) location.reload();
  else status.textContent = "Sign-in refused: " + (await res.text());
});

btn.addEventListener("click", async () => {
  status.textContent = "Pick Meteor Wallet (or another NEAR wallet) in the list…";
  try {
    await connector.connect({ signMessageParams: { message: "Sign in to Doorlist as a host", recipient: btn.dataset.recipient, nonce } });
  } catch (e) {
    status.textContent = "The wallet didn't sign: " + (e && e.message ? e.message : e);
  }
});
