# CometBFT block commit signatures: exact encoding for verification

This document is derived from the [CometBFT](https://github.com/cometbft/cometbft) repo (v0.38.x) and specifies **exactly** how commit signatures are encoded and how to verify them.

## 1. Where signatures live in the block

- **Commit** for block **H** is stored in **block H+1** as `LastCommit`.
- **Commit** (proto: `tendermint.types.Commit`) has:
  - `height` (int64)
  - `round` (int32)
  - `block_id` (BlockID: hash + part_set_header)
  - **`signatures`** (repeated **CommitSig**)

- **CommitSig** (proto: `tendermint.types.CommitSig`) has:
  - `block_id_flag` (BlockIDFlag: Unknown / Absent / Commit / Nil)
  - `validator_address` (bytes, 20 bytes = consensus address)
  - `timestamp` (google.protobuf.Timestamp)
  - **`signature`** (bytes) — **raw Ed25519 signature**, 64 bytes typically

So each entry in `commit.signatures` is one validator’s attestation; `signature` is the bytes to pass to Ed25519 verify.

## 2. What is signed (VoteSignBytes)

Validators sign a **precommit vote**. The signed message is **not** the block content; it is the **canonical serialization of that vote** (CanonicalVote), then **length‑prefixed**:

```
signBytes = MarshalDelimited(CanonicalizeVote(chainID, vote))
```

- **CanonicalizeVote(chainID, vote)** builds a **CanonicalVote** from the vote and chain ID (no validator address/index, no extension fields).
- **MarshalDelimited(msg)** = **varint(len(proto)) || proto.Marshal(msg)** (see CometBFT `libs/protoio/writer.go`).

So:

1. Build the **CanonicalVote** (see below).
2. Protobuf-encode it.
3. Prepend the **varint** encoding of the length of that encoding.
4. The result is **signBytes**. The validator signs **signBytes** with Ed25519 (no SHA256).

Verification: `Ed25519.Verify(pubkey, signBytes, signature)`.

References:
- `types/vote.go`: `VoteSignBytes(chainID, vote)` → `protoio.MarshalDelimited(&CanonicalizeVote(chainID, vote))`.
- `types/vote.go`: `vote.Verify(chainID, pubKey)` → `pubKey.VerifySignature(VoteSignBytes(chainID, v), vote.Signature)` (raw sign bytes, no hash).

## 3. CanonicalVote (proto)

From `proto/tendermint/types/canonical.proto`:

```protobuf
message CanonicalVote {
  SignedMsgType type = 1;           // 0x02 for precommit
  sfixed64 height = 2;
  sfixed64 round = 3;
  CanonicalBlockID block_id = 4;
  google.protobuf.Timestamp timestamp = 5;
  string chain_id = 6;
}

message CanonicalBlockID {
  bytes hash = 1;                        // 32 bytes block hash
  CanonicalPartSetHeader part_set_header = 2;
}

message CanonicalPartSetHeader {
  uint32 total = 1;
  bytes hash = 2;                        // 32 bytes
}
```

- **SignedMsgType**: Prevote = 1, **Precommit = 2**.
- **height** / **round**: **sfixed64** (little‑endian 64‑bit).
- **block_id**: For a non‑nil precommit, use the commit’s `block_id`; for nil vote, use an empty BlockID (no hash, part_set_header with total=0 and empty hash).
- **timestamp**: From the **CommitSig** for that validator (each signer can have a different timestamp).
- **chain_id**: String (e.g. `"gonka"`).

Field order and types must match the proto (field number and wire type) so the binary encoding matches CometBFT.

## 4. Reconstructing the Vote from a CommitSig

To verify one signature in `commit.signatures[i]`:

- **Commit** gives you: `height`, `round`, `block_id`.
- **CommitSig** gives you: `block_id_flag`, `validator_address`, `timestamp`, `signature`.

The logical “vote” for that entry is:

- **type** = Precommit (0x02)
- **height** = commit.height
- **round** = commit.round
- **block_id** = commit.block_id (same for all signers of this commit)
- **timestamp** = this CommitSig’s timestamp
- **validator_address** / **validator_index** = from CommitSig / validator set (not in CanonicalVote)
- **signature** = this CommitSig’s signature

CanonicalVote uses **commit.height**, **commit.round**, **commit.block_id**, **this sig’s timestamp**, and **chain_id**. So when building sign bytes for the i‑th signature, use the i‑th CommitSig’s **timestamp** (and that CommitSig’s **signature** for verification).

## 5. MarshalDelimited (length prefix)

CometBFT uses **varint length prefix** (backwards‑compatible with Amino):

- Encode CanonicalVote with protobuf.
- Prepend **one varint** = length of that encoded message (number of bytes).
- **signBytes = varint(length) || encoded_CanonicalVote**.

So the verifier must:

1. Build CanonicalVote (same fields and encoding as CometBFT).
2. Protobuf‑serialize it (same field numbers, sfixed64 for height/round, etc.).
3. Prepend the **varint** of `encoded_CanonicalVote.length`.
4. Run **Ed25519.Verify(pubkey, signBytes, signature)**.

Do **not** hash signBytes before verify (CometBFT signs the raw sign bytes). Some older or alternate implementations might sign SHA256(signBytes); if raw verify fails, trying SHA256(signBytes) as the message is a possible fallback for compatibility only.

## 6. Ed25519 and address

- **Signature**: Raw Ed25519, typically 64 bytes (CometBFT docs).
- **Address** (consensus): First 20 bytes of **SHA256(raw_32_byte_ed25519_pubkey)**.
- CometBFT uses **zip215** for Ed25519 verification (see encoding spec); most libraries (e.g. `@noble/ed25519`) are compatible.

## 7. Summary checklist for verifiers

1. Get **Commit** for block H from **block H+1**’s `lastCommit`.
2. Get **validator set at height H** (to map `validator_address` / index to pubkey).
3. For each non‑empty **CommitSig** in `commit.signatures`:
   - Resolve **pubkey** from validator set (by address or index).
   - Build **CanonicalVote**: type=Precommit(2), height=commit.height, round=commit.round, block_id=commit.block_id, timestamp=**this** CommitSig’s timestamp, chain_id=chainID.
   - Encode as protobuf (CanonicalBlockID / CanonicalPartSetHeader for block_id; sfixed64 for height/round).
   - **signBytes = varint(len(encoded)) || encoded**.
   - **ok = Ed25519.Verify(pubkey, signBytes, CommitSig.signature)**.
4. Sum voting power of validators whose signatures verify; require **signed_power >= (2/3) * total_power**.

## 8. CometBFT source references

| What | Where (cometbft repo) |
|------|------------------------|
| Commit / CommitSig proto | `proto/tendermint/types/types.proto` |
| CanonicalVote proto | `proto/tendermint/types/canonical.proto` |
| CanonicalizeVote | `types/canonical.go` |
| VoteSignBytes | `types/vote.go` (MarshalDelimited(CanonicalizeVote(...))) |
| Verify (vote) | `types/vote.go` (VerifySignature(VoteSignBytes(...), signature)) |
| MarshalDelimited | `libs/protoio/writer.go` (varint length + proto.Marshal) |
| Encoding / signing spec | [Encoding](https://docs.cometbft.com/v0.38/spec/core/encoding), [Validator Signing](https://docs.cometbft.com/v0.38/spec/consensus/signing) |
