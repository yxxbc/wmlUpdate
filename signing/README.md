# Release signing key

This key signs the **WML Update project's own release checksums**, not the
updates of applications that embed the library. Each application should generate
its own key with `wml-update keygen`.

## Public key

File: [`release-signing-key.pub.pem`](release-signing-key.pub.pem)

DER SubjectPublicKeyInfo SHA-256 fingerprint:

```text
082733d41a311c633e30d4a5603822db65a48ef12be498e38a0a22edeb6a9b55
```

Releases include:

- `checksums.txt`
- `checksums.txt.sig` — detached Ed25519 signature over the exact checksum bytes

## Verify a release

With an existing `wml-update` binary:

```bash
wml-update verify --public-key-file signing/release-signing-key.pub.pem checksums.txt
```

Independent OpenSSL verification:

```bash
openssl base64 -d -A -in checksums.txt.sig -out /tmp/checksums.sig
openssl pkeyutl -verify -pubin \
  -inkey signing/release-signing-key.pub.pem \
  -rawin -in checksums.txt -sigfile /tmp/checksums.sig
```

Download the public key from the same tagged release or a commit you trust, not
from an unverified mirror.

## Private key storage

The private key must never be committed, uploaded as an artifact, pasted into an
issue, or printed in CI logs. Its authoritative CI copy is the encrypted GitHub
Actions secret:

```text
Repository secret: WML_SIGNING_KEY
Repository variable: WML_SIGNING_ENABLED=true
```

A working local backup is stored outside the repository at:

```text
~/.config/wml-update/release-signing-key.pem
```

Permissions are `0600`. The key is also stored in the macOS login Keychain as a
generic password:

```text
service: wml-update-release-signing-key
account: yxxbc
```

Retrieve it with (macOS prints generic-password data as hex when it contains
line breaks):

```bash
security find-generic-password \
  -s wml-update-release-signing-key -w | xxd -r -p \
  > release-signing-key.pem
chmod 600 release-signing-key.pem
```

The login Keychain item is local to this Mac unless iCloud Keychain is enabled
and the item is added through Apple Passwords/Keychain Access. **Move a copy into
Apple Passwords (secure note/password) or another password manager and keep an
encrypted offline backup.** GitHub secrets are write-only: if the private key is
lost, it cannot be recovered from GitHub.

## Rotation

To rotate:

1. Generate a new key pair with `wml-update keygen`.
2. Publish a release whose notes state the new fingerprint before switching the
   channel, and keep the old public key verifiable for historical releases.
3. Update `WML_SIGNING_KEY`, the public key file and the fingerprint above.
4. Retain the old public key for verification of prior releases.

Never overwrite the private key without an offline backup.
