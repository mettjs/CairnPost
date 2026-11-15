// Hands each email Cloudflare receives for your addresses on to a
// CairnPost server, signed, as it came.
//
// Settings (wrangler.toml, or the dashboard):
//   CAIRNPOST_URL             the server's address, such as https://mail.example.com
//   CAIRNPOST_INBOUND_SECRET  the same secret the server holds; set it as a secret
//   FALLBACK_ADDRESS          optional: a verified address to forward an email to
//                             when the server does not take it
//
// An email the server does not take is forwarded to FALLBACK_ADDRESS if
// one is set, and refused otherwise, so its sender is told. It is never
// dropped unsaid.

const TRIES = 4;
// How long to wait before a try when the server does not say: longer
// each time. What the server asks for (Retry-After) is waited instead,
// up to MOST_WAIT_MS: a Worker cannot wait long.
const WAIT_MS = 2000;
const MOST_WAIT_MS = 10000;

// An address as a header carries it: whatever is not a printable ASCII
// character, and "%", written as its UTF-8 bytes in "%" and two hex
// digits. The server reads it back; what is signed is what is sent.
export const written = (address) => (address ?? "").replace(/[^\x21-\x24\x26-\x7e]/g, (c) => encodeURIComponent(c));

const hex = (bytes) => [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");

// The signature a CairnPost server checks: "v1=" and the HMAC-SHA256, in
// hex, of the four parts, each behind its length as eight bytes, big end
// first.
//
// One case to check any change against (the server's tests pin the same):
//   secret     inbound-secret-for-tests-0123456789
//   timestamp  1893456000
//   to         help@mail.example.com
//   from       ana@example.org
//   digest     152f17ac7a38f5d25376dd86328a96a17551fa061a49aa32649ee50c07d9c7c2
//   signature  v1=dc8231a625ddbd1fbcd83ad55995dac3ad63c7c46a6e1df33474467e40daff28
export async function sign(secret, timestamp, to, from, digest) {
  const enc = new TextEncoder();
  const parts = [timestamp, to, from, digest].map((p) => enc.encode(p));
  const signed = new Uint8Array(parts.reduce((n, p) => n + 8 + p.length, 0));
  const view = new DataView(signed.buffer);
  let at = 0;
  for (const p of parts) {
    view.setBigUint64(at, BigInt(p.length));
    signed.set(p, at + 8);
    at += 8 + p.length;
  }
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return "v1=" + hex(await crypto.subtle.sign("HMAC", key, signed));
}

// handOn posts the email and says whether the server took it. A refusal
// that trying again cannot change (4xx) is not tried again.
async function handOn(env, message, raw) {
  const digest = hex(await crypto.subtle.digest("SHA-256", raw));
  // A mail server's own notice, such as a bounce, comes from nobody.
  const to = written(message.to), from = written(message.from);
  let wait = 0;
  for (let n = 0; n < TRIES; n++) {
    if (n > 0) await new Promise((done) => setTimeout(done, wait || WAIT_MS * n));
    // Signed anew each time: a signature is good for a few minutes.
    const timestamp = String(Math.floor(Date.now() / 1000));
    try {
      const res = await fetch(env.CAIRNPOST_URL.replace(/\/+$/, "") + "/inbound", {
        method: "POST",
        headers: {
          "Content-Type": "message/rfc822",
          "X-CairnPost-Timestamp": timestamp,
          "X-CairnPost-To": to,
          "X-CairnPost-From": from,
          "X-CairnPost-Digest": digest,
          "X-CairnPost-Signature": await sign(env.CAIRNPOST_INBOUND_SECRET, timestamp, to, from, digest),
        },
        body: raw,
      });
      if (res.ok) return true;
      // Said without an address: only what the server answered.
      console.log(`cairnpost answered ${res.status} (try ${n + 1} of ${TRIES})`);
      if (res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429) return false;
      const asked = Number(res.headers.get("Retry-After")) * 1000;
      wait = asked > 0 ? Math.min(asked, MOST_WAIT_MS) : 0;
    } catch (err) {
      wait = 0;
      console.log(`cairnpost was not reached (try ${n + 1} of ${TRIES})`);
    }
  }
  return false;
}

export default {
  async email(message, env) {
    const raw = await new Response(message.raw).arrayBuffer();
    if (await handOn(env, message, raw)) return;
    if (env.FALLBACK_ADDRESS) {
      try {
        await message.forward(env.FALLBACK_ADDRESS);
        return;
      } catch (err) {
        // Not a verified destination, or it would not take it: refused,
        // so that the sender is told.
        console.log("the fallback address did not take the email");
      }
    }
    message.setReject("This address cannot take mail right now. Try again later.");
  },
};
