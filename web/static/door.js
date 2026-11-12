// door.js: turns a phone into a Doorlist door.
// 1. Makes an ed25519 key in the browser (WebCrypto) and keeps it for this event.
// 2. Registers the public key as a door for the event (once; the host's wallet signs it in live mode).
// 3. Scans ticket QR codes and signs each check-in with the door key.
(() => {
  const root = document.getElementById("door");
  const ev = root.dataset.event;
  const setup = document.getElementById("setup");
  const result = document.getElementById("result");
  const keyline = document.getElementById("keyline");
  const video = document.getElementById("cam");
  const canvas = document.getElementById("frame");
  const B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";

  function b58(bytes) {
    let digits = [0];
    for (const b of bytes) {
      let carry = b;
      for (let i = 0; i < digits.length; i++) { carry += digits[i] << 8; digits[i] = carry % 58; carry = (carry / 58) | 0; }
      while (carry) { digits.push(carry % 58); carry = (carry / 58) | 0; }
    }
    let out = "";
    for (const b of bytes) { if (b !== 0) break; out += "1"; }
    for (let i = digits.length - 1; i >= 0; i--) out += B58[digits[i]];
    return out;
  }

  function say(title, body, action) {
    setup.hidden = false;
    setup.innerHTML = "";
    const h = document.createElement("p"); h.className = "display sm"; h.textContent = title; setup.append(h);
    const p = document.createElement("p"); p.className = "fine"; p.textContent = body; setup.append(p);
    if (action) setup.append(action);
  }

  const storeKey = "doorlist.door." + ev;
  let priv, pubStr;

  async function loadKey() {
    if (!crypto.subtle) throw new Error("insecure");
    const saved = localStorage.getItem(storeKey);
    if (saved) {
      const { d, x } = JSON.parse(saved);
      priv = await crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", d, x, ext: true }, { name: "Ed25519" }, false, ["sign"]);
      pubStr = "ed25519:" + b58(Uint8Array.from(atob(x.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - x.length % 4) % 4)), c => c.charCodeAt(0)));
      return;
    }
    const kp = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
    const jwk = await crypto.subtle.exportKey("jwk", kp.privateKey);
    localStorage.setItem(storeKey, JSON.stringify({ d: jwk.d, x: jwk.x }));
    priv = kp.privateKey;
    const raw = new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey));
    pubStr = "ed25519:" + b58(raw);
  }

  async function register() {
    const keys = (root.dataset.keys || "").split(",");
    if (keys.includes(pubStr)) return true;
    const res = await fetch(`/door/${ev}/register`, { method: "POST", body: new URLSearchParams({ key: pubStr }) });
    if (res.status === 204) return true;
    if (res.status === 202) {
      const call = await res.json();
      const btn = document.createElement("button");
      btn.className = "btn"; btn.textContent = "Register this phone with my wallet";
      btn.onclick = () => walletRegister(call).catch(e => say("Wallet didn't sign", e.message || String(e)));
      say("One signature to go", "Your wallet adds this phone's key as a door for the event. You only do this once per phone.", btn);
      return false;
    }
    throw new Error(await res.text());
  }

  async function walletRegister(call) {
    const { NearConnector } = await import("https://cdn.jsdelivr.net/npm/@hot-labs/near-connect@0.11.4/+esm");
    const connector = new NearConnector({ network: root.dataset.network || "testnet" });
    let wallet;
    try { wallet = await connector.wallet(); } catch { await connector.connect(); wallet = await connector.wallet(); }
    await wallet.signAndSendTransaction({
      receiverId: call.contract,
      actions: [{ type: "FunctionCall", params: { methodName: call.method, args: call.args, gas: "30000000000000", deposit: "0" } }],
    });
    location.reload();
  }

  async function sign(ticket) {
    const at = Date.now();
    const msg = new TextEncoder().encode(`doorlist:checkin:v1|${ev}|${ticket}|${at}`);
    const sig = new Uint8Array(await crypto.subtle.sign({ name: "Ed25519" }, priv, msg));
    return { event: ev, ticket, at_ms: at, door_key: pubStr, signature: b58(sig) };
  }

  let busy = false, last = "", lastAt = 0;
  async function checkIn(ticket) {
    if (busy || (ticket === last && Date.now() - lastAt < 4000)) return;
    busy = true; last = ticket; lastAt = Date.now();
    try {
      const res = await fetch(`/door/${ev}/scan`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(await sign(ticket)) });
      result.innerHTML = await res.text();
      const hit = result.querySelector("[data-shows]");
      if (hit) document.getElementById("count").innerHTML = `<b>${hit.dataset.shows}</b> / ${hit.dataset.going} in`;
      if (navigator.vibrate) navigator.vibrate(hit && hit.classList.contains("ok") ? 60 : [40, 60, 40]);
    } catch (e) {
      result.innerHTML = "";
      const d = document.createElement("div"); d.className = "hit no";
      d.innerHTML = '<p class="display lg">No connection.</p><p class="fine">The scan wasn\'t sent. Check the phone\'s signal and scan again.</p>';
      result.append(d);
    } finally { busy = false; }
  }

  function parse(text) {
    const m = /\/t\/([a-z0-9-]{3,40})\/([A-Z2-7]{8,32})\/?$/.exec(text.trim());
    if (!m) return null;
    return { event: m[1], ticket: m[2] };
  }

  async function startCamera() {
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" }, audio: false });
    } catch {
      say("Camera is blocked", "Allow camera access for this site in the browser settings and reload. Until then, type each guest's code below.");
      return;
    }
    video.srcObject = stream;
    await video.play();
    setup.hidden = true;
    let detect;
    if ("BarcodeDetector" in window) {
      const bd = new BarcodeDetector({ formats: ["qr_code"] });
      detect = async () => { const c = await bd.detect(video); return c[0] && c[0].rawValue; };
    } else {
      await new Promise((ok, bad) => { const s = document.createElement("script"); s.src = "/static/vendor/jsQR.js"; s.onload = ok; s.onerror = bad; document.head.append(s); });
      const ctx = canvas.getContext("2d", { willReadFrequently: true });
      detect = async () => {
        const w = video.videoWidth, h = video.videoHeight;
        if (!w) return null;
        const size = Math.min(w, h, 720);
        canvas.width = size; canvas.height = size;
        ctx.drawImage(video, (w - Math.min(w, h)) / 2, (h - Math.min(w, h)) / 2, Math.min(w, h), Math.min(w, h), 0, 0, size, size);
        const code = window.jsQR(ctx.getImageData(0, 0, size, size).data, size, size, { inversionAttempts: "dontInvert" });
        return code && code.data;
      };
    }
    const tick = async () => {
      try {
        const text = await detect();
        if (text) {
          const t = parse(text);
          if (!t) { if (text !== last) { last = text; lastAt = Date.now(); result.innerHTML = '<div class="hit no"><p class="display lg">Not a ticket.</p><p class="fine">That QR code isn\'t a Doorlist ticket.</p></div>'; } }
          else if (t.event !== ev) { result.innerHTML = '<div class="hit no"><p class="display lg">Wrong event.</p><p class="fine">That ticket is for another Doorlist event.</p></div>'; }
          else await checkIn(t.ticket);
        }
      } catch {}
      setTimeout(tick, 220);
    };
    tick();
  }

  document.getElementById("manual").addEventListener("submit", async (e) => {
    e.preventDefault();
    const code = document.getElementById("code").value;
    const res = await fetch(`/door/${ev}/lookup?code=${encodeURIComponent(code)}`);
    if (!res.ok) { result.innerHTML = await res.text(); return; }
    const { ticket } = await res.json();
    last = ""; await checkIn(ticket);
    document.getElementById("code").value = "";
  });

  (async () => {
    try { await loadKey(); }
    catch {
      say("This browser can't hold a door key", "It needs Ed25519 in WebCrypto: a current Chrome, Safari or Firefox over HTTPS (or localhost).");
      return;
    }
    keyline.textContent = "This phone's door key: " + pubStr;
    try { if (!(await register())) return; }
    catch (e) { say("Couldn't register this door", String(e.message || e)); return; }
    await startCamera();
  })();
})();
