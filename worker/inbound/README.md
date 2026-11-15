# CairnPost inbound Worker

A Cloudflare Email Worker that hands each email Cloudflare receives for
your addresses on to a CairnPost server, as it came, signed with a secret
the two share. It reads nothing of the email and has no dependencies.

## Set it up

1. On the CairnPost server, set `CAIRNPOST_INBOUND_SECRET` to a secret of
   at least 32 characters (`openssl rand -hex 32`) and restart it.
   `GET /health` then says `"inbound": "on"`.
2. Here: copy `wrangler.toml.example` to `wrangler.toml` and set
   `CAIRNPOST_URL` to the server's address.
3. `npx wrangler secret put CAIRNPOST_INBOUND_SECRET` and give it the
   same secret.
4. `npx wrangler deploy`.
5. In the Cloudflare dashboard, under Email Routing for your domain, send
   the addresses you want, or the catch-all, to this Worker.

Send an email to one of those addresses: it appears under Emails,
Received, in the CairnPost console, and your endpoints get an
`email.received` webhook.

## When the server does not take an email

The Worker tries four times, some seconds apart, waiting as long as the
server asks where it says. A refusal that trying again cannot change (the
secret is wrong, the address is none) is not tried again. If the server
does not take the email:

- with `FALLBACK_ADDRESS` set to a verified destination address, the
  email is forwarded there;
- without one, or when that address does not take it, the email is
  refused, and the sending server tells its
  sender it did not arrive.

It is never dropped unsaid. The Worker's log (`npx wrangler tail`) says
what the server answered, and never an address.

## Checking a change

`node selfcheck.mjs` signs one fixed case and compares it with the
signature the server's own tests pin.
