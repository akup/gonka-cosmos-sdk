package slashing

import (
	"context"
	"fmt"

	"cosmossdk.io/core/comet"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// BeginBlocker check for infraction evidence or downtime of validators
// on every begin block
func BeginBlocker(ctx context.Context, k keeper.Keeper) error {
	defer telemetry.ModuleMeasureSince(types.ModuleName, telemetry.Now(), telemetry.MetricKeyBeginBlocker)

	// Iterate over all the validators which *should* have signed this block
	// store whether or not they have actually signed it and slash/unbond any
	// which have missed too many blocks in a row (downtime slashing)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	voteInfos := sdkCtx.VoteInfos()

	// Log expected validator consensus addresses (e.g. for debugging snapshot restoration)
	expectedAddrs := make([]string, 0, len(voteInfos))
	for _, voteInfo := range voteInfos {
		expectedAddrs = append(expectedAddrs, fmt.Sprintf("%X", voteInfo.Validator.Address))
	}
	k.Logger(ctx).Info(
		"processing validator signatures (HandleValidatorSignature)",
		"height", sdkCtx.BlockHeight(),
		"expected_validator_cons_addrs", expectedAddrs,
		"count", len(expectedAddrs),
	)

	// Log all validators in staking (for debugging snapshot restoration and index/signing info)
	allValidators, err := k.GetAllValidatorsFromStaking(ctx)
	if err == nil {
		allConsAddrs := make([]string, 0, len(allValidators))
		for _, v := range allValidators {
			consAddr, err := v.GetConsAddr()
			if err != nil {
				continue
			}
			allConsAddrs = append(allConsAddrs, fmt.Sprintf("%X", consAddr))
		}
		k.Logger(ctx).Info(
			"all validators in staking (GetAllValidators)",
			"height", sdkCtx.BlockHeight(),
			"all_validator_cons_addrs", allConsAddrs,
			"count", len(allConsAddrs),
		)
	}

	// RestoreValidatorIndex repopulates staking's by-cons-addr and by-power indexes when they
	// are missing (e.g. after snapshot restore), so that ValidatorByConsAddr and other lookups work.
	k.RestoreValidatorIndex(ctx)
	for _, voteInfo := range voteInfos {
		err := k.HandleValidatorSignature(ctx, voteInfo.Validator.Address, voteInfo.Validator.Power, comet.BlockIDFlag(voteInfo.BlockIdFlag))
		if err != nil {
			return err
		}
	}
	return nil
}
