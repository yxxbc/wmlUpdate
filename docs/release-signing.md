# Release signing

The project's own release checksums are signed with an Ed25519 key. This key is
for **WML Update releases**, not for applications embedding the library. Every
application should generate its own key with `wml-update keygen`.

## Public key

- [Public key](https://github.com/yxxbc/wmlUpdate/blob/main/signing/release-signing-key.pub.pem)
- Raw form: `https://raw.githubusercontent.com/yxxbc/wmlUpdate/main/signing/release-signing-key.pub.pem`
- DER SubjectPublicKeyInfo SHA-256 fingerprint:

```text
082733d41a311c633e30d4a5603822db65a48ef12be498e38a0a22edeb6a9b55
```

Each release includes `checksums.txt` and `checksums.txt.sig`, a detached
signature over the exact checksum file bytes.

## Verify

With an existing binary:

```bash
wml-update verify \
  --public-key-file signing/release-signing-key.pub.pem \
  checksums.txt
```

Independently with OpenSSL:

```bash
openssl base64 -d -A -in checksums.txt.sig -out /tmp/checksums.sig
openssl pkeyutl -verify -pubin \
  -inkey signing/release-signing-key.pub.pem \
  -rawin -in checksums.txt -sigfile /tmp/checksums.sig
```

Fetch the public key from the tagged release or a trusted commit, never from an
unverified mirror.

## Private key custody

The private key must never enter git, an artifact, a shell transcript, an issue
or an unencrypted backup.

| Copy | Location |
| --- | --- |
| CI | GitHub Actions secret `WML_SIGNING_KEY`; enable with variable `WML_SIGNING_ENABLED=true` |
| Local macOS backup | `~/.config/wml-update/release-signing-key.pem` (mode `0600`) |
| macOS Keychain | service `wml-update-release-signing-key`, account `yxxbc` |

GitHub secrets are write-only. Store the key in a password manager or encrypted
offline backup as well. A lost private key cannot be recovered from GitHub.

### Retrieve from macOS Keychain

`security` returns generic-password data as hex when it contains line breaks:

```bash
security find-generic-password \
  -s wml-update-release-signing-key -w | xxd -r -p \
  > release-signing-key.pem
chmod 600 release-signing-key.pem
```

The login Keychain item is local to the Mac unless iCloud Keychain is enabled
and the item is added through Apple Passwords/Keychain Access. For cross-device
recovery, also store the PEM in Apple Passwords (as a secure note/password) or a
password manager.

## Rotation

1. Generate a new pair with `wml-update keygen`.
2. Publish the new public key and fingerprint before switching the CI secret.
3. Keep the old public key available for verifying historical releases.
4. Update `WML_SIGNING_KEY` and `WML_SIGNING_ENABLED`.
5. Never overwrite the private key without an offline backup.
