package types

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sync"

	cmtprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	"cosmossdk.io/store/internal/maps"
)

// lastCommitInfoHashLoggedVersion: APP_HASH_DEBUG for Hash() is emitted at most once per block.
var (
	lastCommitInfoHashLoggedVersion int64 = -1
	commitInfoHashLogMu             sync.Mutex
)

// GetHash returns the GetHash from the CommitID.
// This is used in CommitInfo.Hash()
//
// When we commit to this in a merkle proof, we create a map of storeInfo.Name -> storeInfo.GetHash()
// and build a merkle proof from that.
// This is then chained with the substore proof, so we prove the root hash from the substore before this
// and need to pass that (unmodified) as the leaf value of the multistore proof.
func (si StoreInfo) GetHash() []byte {
	return si.CommitId.Hash
}

func (ci CommitInfo) toMap() map[string][]byte {
	m := make(map[string][]byte, len(ci.StoreInfos))
	for _, storeInfo := range ci.StoreInfos {
		m[storeInfo.Name] = storeInfo.GetHash()
	}

	return m
}

// Hash returns the simple merkle root hash of the stores sorted by name.
func (ci CommitInfo) Hash() []byte {
	// [APP_HASH_DEBUG] Log at most once per block (per version).
	commitInfoHashLogMu.Lock()
	shouldLog := lastCommitInfoHashLoggedVersion != ci.Version
	if shouldLog {
		lastCommitInfoHashLoggedVersion = ci.Version
	}
	commitInfoHashLogMu.Unlock()

	if shouldLog {
		fmt.Fprintf(os.Stderr, "[APP_HASH_DEBUG] CommitInfo.Hash() ENTERED version=%d num_store_infos=%d\n", ci.Version, len(ci.StoreInfos))
	}
	// we need a special case for empty set, as SimpleProofsFromMap requires at least one entry
	if len(ci.StoreInfos) == 0 {
		emptyHash := sha256.Sum256([]byte{})
		return emptyHash[:]
	}

	m := ci.toMap()
	if shouldLog {
		for name, h := range m {
			fmt.Fprintf(os.Stderr, "[APP_HASH_DEBUG] CommitInfo.Hash() store name=%q iavl_root_hex=%X\n", name, h)
		}
	}

	rootHash, _, _ := maps.ProofsFromMap(m, ci.Version)

	if len(rootHash) == 0 {
		emptyHash := sha256.Sum256([]byte{})
		return emptyHash[:]
	}

	if shouldLog {
		fmt.Fprintf(os.Stderr, "[APP_HASH_DEBUG] CommitInfo.Hash() version=%d multistore_root_hex=%X\n", ci.Version, rootHash)
	}
	return rootHash
}

func (ci CommitInfo) ProofOp(storeName string) cmtprotocrypto.ProofOp {
	ret, err := ProofOpFromMap(ci.toMap(), storeName)
	if err != nil {
		panic(err)
	}
	return ret
}

func (ci CommitInfo) CommitID() CommitID {
	return CommitID{
		Version: ci.Version,
		Hash:    ci.Hash(),
	}
}
