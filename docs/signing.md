# Module digests and signing

Signed modules follow the Rust sibling's `docs/signing.md`, with a
hybrid (post-quantum) signature added beside its Ed25519 one. The code is
`pgauth/modsig`. Keys, certificates and key lists are those of
`docs/auth.md`.

## Digest

SHA-256 over the module with every `purgatorio.sig` custom section cut
out, exactly as the Rust sibling defines it: the 8-byte header, then each
section from its id byte to its last byte, as written, skipping whole
each custom section named `purgatorio.sig`. Sizes are LEB128 of at most 5
bytes (a fifth byte at most `0x0f`), and the section list must end at
the end of the file. Bytes that break these rules are not a module
(`exec header invalid`, Inferno's wording, where the Rust sibling says
`bad executable`).

## The `purgatorio.sig` section

```
version u8          1: classical entries only (the Rust format)
                    2: at least one hybrid entry
count   u8          1 to 8 entries
entry   ...         count times

entry  = kind u8 || signature || body
kind 0: Ed25519 signature[64],  body = key[32]
kind 1: Ed25519 signature[64],  body = ncerts u8 (1-4),
        then per certificate: len u16 LE (1-2048) || pgcert1 line
kind 2: hybrid signature[3373], body = hybrid key[1984]
kind 3: hybrid signature[3373], body = ncerts u8 (1-4),
        then per certificate: len u16 LE (1-16384) || pgcert2 line
```

The signing key is the key (kinds 0 and 2) or the first certificate's
key (kinds 1 and 3). Each later certificate certifies a key that signed
the one before it. A classical entry signs

```
"pgmodsig1" 0x00 || digest[32] || len(abi) u8 || abi
```

and a hybrid entry signs the same with `pgmodsig2`, where `abi` is the
module's first `purgatorio.abi` section, or empty. A hybrid entry's chain
holds only `pgcert2` certificates, a classical entry's only `pgcert1`.

A section is version 1 exactly when all its entries are classical, so a
module signed only with Ed25519 keys is byte-identical to what the Rust
sibling's tools write (`pgauth/testdata/rust` holds modules the Rust
tools signed, and the tests make the same bytes). A section with a
hybrid entry is version 2, which the Rust loader refuses as an unknown
version. Parsing is strict: an unknown version or kind, a count or length
out of range, a duplicate signing key, a bad certificate, leftover bytes
or a version that does not match the entries make the section bad.

Signatures are deterministic in both families (Ed25519, and ML-DSA's
deterministic variant), so signing the same module with the same key
gives the same bytes on every host.

## Trust

As in the Rust sibling: `/lib/keys/modules` holds the anchors (classical
keys, or `pq:` fingerprints of hybrid keys) and `/lib/keys/revoked` the
revoked keys and module digests; a damaged revocation list refuses
everything. An entry is trusted when its signature verifies and its key
is an anchor, or a chain of valid, unexpired, unrevoked certificates
links it to one.

**Classical entries count only when the policy says so.** By default a
policy takes hybrid entries alone; an Ed25519 entry that would be trusted
then stands as `classical`, and a module whose best entry is one fails
with `hybrid signature required`. A policy that names `classical` takes
both (see "Policy"). The loader's errors, the first that applies:

| error | meaning |
|---|---|
| `module not signed` | there is no `purgatorio.sig` section |
| `module revoked` | the module's digest is revoked |
| `bad signature` | the section is malformed or repeated, or no entry verifies |
| `hybrid signature required` | the only entries that would pass are classical, and the policy does not take them |
| `certificate revoked` | an entry verifies, but a key in it or its anchor is revoked, or the list is damaged |
| `certificate expired` | an entry verifies, but a certificate in its chain has expired |
| `untrusted signer` | an entry verifies, but nothing links it to an anchor |

## Policy

A prog's module policy is set as in the Rust sibling, by writing to its
`ctl` file, and only tightens:

```
echo modules require > /prog/<pid>/ctl              hybrid signatures only
echo modules require classical > /prog/<pid>/ctl    hybrid or Ed25519
echo modules warn [classical] > /prog/<pid>/ctl
```

`require classical` is looser than `require`, so it may follow `none` or
`warn`, and `require` may follow it, never the reverse. `emu
--modules=warn|require[,classical]` sets the first prog's policy.
(This part lands with the loader; see "Status".)

## Ahead-of-time images

The signed image file is the Rust sibling's:

```
magic   "PURGIMG1"
kind    u8          the image's target (the Rust sibling: 0 native aarch64, 1 Pulley)
zero    3 bytes
module  [32]        the module's digest
len     u64 LE
image   len bytes
sig     ...         to the end: a purgatorio.sig section body
```

A classical entry signs

```
"pgaotimg1" 0x00 || kind u8 || module digest[32] || SHA-256(image)[32]
```

and a hybrid entry the same with `pgaotimg2`. The anchors are
`/lib/keys/aot`, read in the boot namespace; the revocation list may name
the module's digest or the image's. As for modules, classical entries
count only when the engine's trust says so.

The Go API for the engine and the tools that write images:

```go
file, err := modsig.BuildImage(kind, moduleDigest, image, key, certs) // sign
file, err  = modsig.SignImage(file, key, certs)                      // countersign
img, err  := modsig.ParseImage(file)
code, err := img.Verify(kind, moduleDigest, &modsig.Trust{
	Anchors: aotList, Revoked: revoked, Damaged: damaged,
	Now: now, Classical: false,
})
```

`modsig.Digest(wasm)` is the module digest to compare with, and
`pgauth.ParseList` reads the key lists. `Verify` returns
`bad AOT image`, `module revoked` or `untrusted AOT image`.

## Modules from Go

```go
signed, err := modsig.Sign(wasm, key, certs)   // add or replace key's entry
info, err := modsig.Inspect(signed)            // digest, ABI, sections
keys, err := modsig.Check(info, &modsig.Trust{Anchors: anchors, Revoked: revoked, Now: now})
```

`modsig.SignEntry` and `modsig.NewEntry` build an entry from a signature
made elsewhere (an agent), and `modsig.Attach` adds it.

## Status

- Landed: the digest, both entry families, the section, the trust check,
  and the image file, with cross tests against the Rust tools' output.
- Next: the per-prog policy in the loader (`spawn_prog`, `load`, the
  first prog), `/prog/<pid>/module`, `emu --modules`, and the tools
  `auth/sign`, `auth/verify -m` and `auth/modpolicy`.
