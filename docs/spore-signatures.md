# Spore signatures

New signatures use format v2. The displayed `content_hash` identifies the
entire canonical payload signed by Ed25519, including `proposed_writes` and
`proposed_edges`. A spore with an empty markdown body still has signed content
when its frontmatter contains proposals.

```bash
hypha spore verify proposal.md --format text
hypha spore verify spore.2026-10-05.example.proposal --format json
```

Verification reads the document and the signer's public identity record. The
entire requested identity URI (authority and name) must match that record;
`signer` reports the resolved record's canonical URI. Verification uses no private key and does not write to the space or index. An id lookup
searches inboxes and accepted documents; use `--space` or a file path to resolve
ambiguous ids. File verification also works for a spore outside an installed
space.

Text results begin with `VALID`, `V0_LEGACY`, `INVALID`, or `UNSIGNED`. Valid
signatures exit with code 0; legacy, invalid, and unsigned documents exit with
code 1. JSON uses the
standard envelope: `ok` is true only for `VALID`, and `data` contains `status`,
`signer`, `version`, `covered_fields`, `excluded_fields`, the recomputed hashes,
and `recorded` signature metadata. `changed` compares body and frontmatter
hashes against recorded values. It identifies differences, not who caused them;
signature metadata may also have been edited. Unknown identities, malformed
signatures, and unsupported versions produce `INVALID` with a diagnostic.

## What is covered

V1 and v2 cover the author agent id, spore id, creation time, authored
markdown body, and all frontmatter substance, including proposals and source
references. Only these parts are excluded:

- The top-level `status`, so review and graft can change it.
- The entire `signature` block, avoiding a circular hash. `signed_at` is
  descriptive metadata, not an authenticated timestamp.
- The appended trace work log, starting at the first literal newline followed
  by `## Work log (trace.` in the body. This preserves the existing trace workflow.

Body content before the work-log marker is hashed byte for byte. Body
whitespace changes affect the hash. YAML formatting changes that preserve
parsed values do not change v2 frontmatter hashes. Changing a block scalar's
chomping style can change its final newline and therefore its substance.

## Format v2

The signature block records:

```yaml
signature:
  version: 2
  alg: ed25519
  key: identity://example/reviewer
  body_hash: sha256:<authored-body-digest>
  frontmatter_hash: sha256:<frontmatter-substance-digest>
  content_hash: sha256:<canonical-payload-digest>
  signed_at: 2026-10-05T12:00:00Z
  value: ed25519:<base64-signature>
```

These are SHA-256 digests. `body_hash` can equal the SHA-256 of empty input when
there is no authored body. `content_hash` still covers the frontmatter through
`frontmatter_hash`, so it describes everything signed.

The encoding is defined as follows:

1. Read the YAML between complete `---` delimiter lines using YAML v3 semantic
   parsing. LF and CRLF delimiter lines are accepted. The body starts after the
   closing delimiter's line ending.
2. Remove only the top-level `status` and `signature` keys. Encode the remaining
   mapping with Go's `encoding/json.Marshal`: compact JSON, recursively sorted
   string keys, and unchanged array order. YAML timestamps become UTC
   RFC3339Nano strings recursively. Scalar types retain their parsed meaning;
   quoted strings remain strings. Duplicate keys, non-string mapping keys, NaN,
   infinity, and other unsupported JSON values fail instead of using a fallback
   digest. No Unicode normalization is applied. JSON uses Go's default escaping,
   including HTML escaping.
3. Hash that JSON for `frontmatter_hash`. Hash the authored body bytes for
   `body_hash`. Both are stored with the `sha256:` prefix.
4. Encode another compact sorted-key JSON object with exactly these keys:
   `agent_id`, `body_hash`, `created`, `frontmatter_hash`, `spore_id`, and `version`.
   `agent_id` and `spore_id` are the trimmed string values from `agent.id` and
   `id`; `created` is UTC RFC3339Nano; `version` is the integer 2. Hash strings
   include their `sha256:` prefixes. The frontmatter hash also covers the
   original parsed values of these fields.
5. Sign those JSON bytes with Ed25519. Their SHA-256 digest is `content_hash`.

The YAML serializer may change indentation or scalar presentation when signing,
but it does not participate in hashing. Verification reconstructs the same
semantic JSON directly, without a synthetic signature or YAML round-trip.

Submit and amend receipts now use this v2 canonical `content_hash`, including
its `sha256:` prefix, for signed and unsigned spores. A v2 signed submission's
receipt hash equals its signature block's hash. Status changes, signature
replacement, and appended trace logs do not change that content identity.
Historical receipts remain unchanged; their bare hexadecimal hashes describe
the serialized file bytes instead.

## Legacy v1

Signatures with no `version`, or with `version: 1`, retain their original
verification rules. The body byte ranges and frontmatter parser are preserved,
as is the historical sorted array of `[key, value]` pairs at the top level.
The legacy payload contains agent id, spore id, UTC RFC3339 creation time, body
hash hex, and frontmatter substance hash hex as newline-separated values, with
a final newline.

V1 already signs frontmatter substance, including `proposed_writes`. Its block's
`content_hash` names only the body hash. The verifier reports this explicitly:

```text
VALID
  Signature version: 1
  v1: content_hash covers body only; frontmatter verified via payload
```

V1 stores no separate frontmatter digest, so a signature failure with a matching
body hash cannot distinguish a frontmatter change from a changed signature.
Existing v1 spores need no migration. Signing a new proposal or explicitly
re-signing an amended proposal writes v2.

## Legacy v0

Before frontmatter hashing was introduced, the payload had four newline-separated
fields: agent id, spore id, UTC RFC3339 creation time, and body hash hex, followed
by a final newline. These blocks usually have no version. The verifier identifies
the format by checking the cryptographic payload, rather than trusting a date or
version label. It reports a distinct result:

```text
V0_LEGACY
  Signature version: 0
  v0 legacy: covers body only; frontmatter and proposals NOT covered
```

This authenticates the authored body and those three identifying fields. It
provides no evidence for proposals, source references, confidence, or other
frontmatter. It never passes full `Verify`; callers receive `ErrLegacyUnverified`.
The CLI exits 1 and JSON has `ok: false`, with the limited scope in `data`.

Graft checks every signature-bearing spore. A v0 preview emits a warning; applying
its proposals requires `--allow-legacy-proposals`, including when `--verify` is
set. This flag allows reviewed v0 proposals only; INVALID signatures are refused.
Unsigned spores remain graftable unless `--verify` requires a signature.

```bash
hypha graft <legacy-spore-id> --as identity://example/reviewer --dry-run --diff
# Review the proposals before explicitly allowing their application:
hypha graft <legacy-spore-id> --as identity://example/reviewer --allow-legacy-proposals
```

Use `hypha spore audit [--space <uri>] [--format text|json]` to count all formats
and list invalid files and proposal mismatches. If a v1 signature matches only
with `proposed_writes`, `proposed_edges`, or both omitted, the current file remains
INVALID and those fields are reported as unverified. This identifies a matching
preimage, not the time or cause of the change. The audit never re-signs or edits
records. Preserve historical files; review their provenance before deciding
whether to create a new, signed proposal.
