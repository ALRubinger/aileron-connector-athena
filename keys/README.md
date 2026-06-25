# Publisher signing key

Aileron's install pipeline (ADR-0004) verifies every connector and
action download against the publisher's ed25519 public key. To trust
this publisher, users add the raw public-key bytes (base64-encoded) to
their `~/.aileron/keyring.json`.

Two artifacts come out of the keypair generation:

- `publisher.pub` (committed here, PEM format) — the canonical, human-
  readable form. Anyone can derive the raw bytes from this.
- The matching ed25519 private key, stored out-of-repo (1Password) and
  base64-encoded into the `AILERON_SIGNING_KEY` GitHub Actions secret.
  The release workflow uses it to sign release tarballs.

## Generating the keypair (publisher one-time setup)

```sh
# 1. Generate. Private key lives in /tmp briefly; public goes here.
openssl genpkey -algorithm ed25519 -out /tmp/publisher.key
openssl pkey -in /tmp/publisher.key -pubout -out keys/publisher.pub

# 2. Encode the private key for the GitHub Actions secret.
#    macOS:
base64 -i /tmp/publisher.key | pbcopy
#    Linux:
# base64 < /tmp/publisher.key | xclip -selection clipboard

# 3. In GitHub: repo Settings → Secrets and variables → Actions →
#    New repository secret. Name: AILERON_SIGNING_KEY. Value: paste.

# 4. Save the PRIVATE key to 1Password (or your password manager of
#    choice), then delete the local file:
rm /tmp/publisher.key

# 5. Commit the public key:
git add keys/publisher.pub
git commit -m "feat(keys): commit publisher signing public key"
git push
```

## Trusting this publisher (consumer side)

Aileron's keyring is JSON at `~/.aileron/keyring.json`. The schema
(`internal/cstore/keyring_config.go::keyringFile`) maps an FQN
authority to a list of base64-encoded **raw 32-byte ed25519 public
keys** — not PEM, not OpenSSH format.

**`publisher.pub` and the keyring entry are the same public key, in two
encodings.** `publisher.pub` is the PEM form (`openssl pkey -pubout`
output — wraps the 32-byte key in an ASN.1/DER SubjectPublicKeyInfo
header). The keyring stores the underlying 32 raw bytes, base64-encoded.
Same key, different packaging — Go's `crypto/ed25519` works with the
raw form directly, so the keyring skips the SPKI wrapping.

Extract the raw form from `publisher.pub`:

```sh
# Decode PEM → DER (44-byte SubjectPublicKeyInfo for ed25519), drop the
# 12-byte ASN.1/DER header, base64 the remaining 32-byte raw key.
PUB_KEY_RAW=$(openssl pkey -in keys/publisher.pub -pubin -outform DER | tail -c 32 | base64)
echo "$PUB_KEY_RAW"
```

Then add it to the keyring (creating the file if it doesn't exist):

```json
{
  "version": 1,
  "publishers": {
    "github://ALRubinger/aileron-connector-athena": [
      "<paste $PUB_KEY_RAW here>"
    ]
  }
}
```

If the file already has other publishers, add a new entry to the
`publishers` map alongside them. Multiple keys per authority are
supported — useful during key rotation (publisher ships under the new
key while consumers still trust both).

Without an entry in the keyring for this authority, `aileron connector
install` fails closed with `ClassSignatureFailure` per ADR-0004's
fail-modes table — unsigned or unverified binaries never reach disk.

## Release-time signing flow

For reference: when a `vX.Y.Z` tag is pushed, `.github/workflows/release.yml`
base64-decodes the `AILERON_SIGNING_KEY` repo secret back to the PEM
private key, signs `connector.wasm || manifest.toml` with
`openssl pkeyutl -sign -rawin`, and attaches the signature to the
release as `connector-payload.sig` (raw 64-byte ed25519 signature).
Aileron's verify path (`internal/cstore/verify.go`) re-derives the same
payload, matches against the publisher's keyring entry, and accepts on
match.
