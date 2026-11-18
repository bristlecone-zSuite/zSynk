# 0016 — Vault and blob stores are encrypted at rest under a passphrase

## Context

Phase 6 calls for encryption of Synk's own store at rest. The store holds
the vault — versioned copies of every file the sync has ever replaced or
deleted, plus quarantined bytes — and the blob store, which holds the
current content of every synced file. Both live in the user's data
directory in plain text. A stolen laptop, a disk image taken from a
backup, or a process with read access to the directory sees everything,
including the version history that outlives the live files.

The transfer path is already protected: peers talk over mutual TLS 1.3
and the relay only ever sees ciphertext. The gap is the disk.

## Decision

The vault and the blob store are sealed under a key derived from a
passphrase.

- **Envelope.** Every sealed file is self-describing: a magic header, a
  random 32-byte base, the plaintext length, then AES-256-GCM chunks of
  1 MiB. Each chunk's nonce is the first 12 bytes of
  `SHA-256(base || chunk index)`, so one key never reuses a nonce across
  files, chunks, or writes. The base is random per file, so two sealed
  copies of the same plaintext differ.
- **Key.** Argon2id (16-byte random salt, four threads) derives the 32
  bytes. A `keycheck.json` file — salt plus a sealed check value — lets
  the node verify a passphrase before it has opened anything, and a
  wrong passphrase fails fast with `ErrVaultLocked` instead of corrupting
  reads.
- **Migration is crash-safe.** Sealed and plain files coexist: a sealed
  file is recognized by its magic and decrypted, a plain file is read as
  is. Enabling seals everything in place, file by file, atomically; a
  crash mid-migration leaves a mixed store that both modes can read.
  Disabling unseals the same way.
- **Quarantine names.** When the vault is encrypted, quarantined content
  is stored under a fixed label instead of the original file name, so
  user content names do not appear on disk in an encrypted vault.
- **Blobs are sealed too.** Sealing only the vault would be theater: the
  blob store holds the live content. The blob store takes a `Sealer`
  (defined in the vault package, so the dependency points one way); the
  content hash stays the plaintext hash, so content stays addressable,
  and every read still verifies it.
- **Surface.** `synk encrypt [--off] [--passphrase PASS]` (prompts on the
  terminal when the flag is omitted), `POST /v1/config/encryption/enable`
  and `POST /v1/config/encryption/disable` with a `{"passphrase"}` body,
  and `vaultEncrypted` in the status. A daemon that must start against an
  encrypted store sets `SYNK_VAULT_PASSPHRASE`. The passphrase is never
  stored; only the salt and the sealed check value are.
- **Dependencies.** `golang.org/x/crypto` (Argon2id) and `golang.org/x/term`
  (the non-echoing prompt). Both are maintained, permissively licensed,
  and security-relevant, which is why this record exists.

## Consequences

- A stolen disk yields ciphertext and a salt. Recovering the data needs
  the passphrase; the salt makes offline guessing the only alternative,
  and Argon2id makes it expensive.
- The passphrase is the single point of failure. Losing it loses the
  version history and the live content on that device; the other devices
  still hold their copies, so a re-pair and re-sync can rebuild the
  library, but the local version history is gone. The CLI and the ADR
  say this plainly; there is no recovery path and pretending otherwise
  would be a lie.
- An encrypted store cannot be read by an older binary that predates
  this change. That is intended: the alternative — silently reading
  ciphertext as plaintext — is how data gets destroyed.
- Enabling takes a lock on the node and rewrites every blob, so it is
  slow on a large library. It is a one-time migration, run deliberately.
- The wire format is unchanged: transfer still moves plaintext over
  mutual TLS, and the relay still sees only ciphertext.
