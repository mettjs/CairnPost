// Checks the Worker's signature against the case the server's tests pin.
// Run with: node worker/inbound/selfcheck.mjs
import { sign, written } from "./worker.js";

for (const [address, want] of [["help@mail.example.com", "help@mail.example.com"], [undefined, ""], ["a b%é@x.example", "a%20b%25%C3%A9@x.example"]]) {
  if (written(address) !== want) {
    console.error(`${address} is written ${written(address)}, not ${want}`);
    process.exit(1);
  }
}

const got = await sign("inbound-secret-for-tests-0123456789", "1893456000", "help@mail.example.com", "ana@example.org",
  "152f17ac7a38f5d25376dd86328a96a17551fa061a49aa32649ee50c07d9c7c2");
const want = "v1=dc8231a625ddbd1fbcd83ad55995dac3ad63c7c46a6e1df33474467e40daff28";
if (got !== want) {
  console.error(`the signature is ${got}, not ${want}`);
  process.exit(1);
}
console.log("the Worker signs as the server checks");
